package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/api/option"
)

// Version is set by the build system.
var Version = "dev"

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

func formatVersion(projectName, version string) string {
	normalized := version
	if semverRe.MatchString(normalized) && !strings.HasPrefix(normalized, "v") {
		normalized = "v" + normalized
	}
	return fmt.Sprintf("%s version %s (%s, %s/%s)", projectName, normalized, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func writeVersion(w io.Writer, projectName, version string) error {
	_, err := fmt.Fprintln(w, formatVersion(projectName, version))
	return err
}

// -----------------------------------------------------------------------------
// Domain types
// -----------------------------------------------------------------------------

// Item is a shopping list entry.
type Item struct {
	ID        string    `json:"id" firestore:"id"`
	Name      string    `json:"name" firestore:"name"`
	Quantity  *string   `json:"quantity,omitempty" firestore:"quantity,omitempty"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}

// ItemInput is the user-facing upsert payload.
type ItemInput struct {
	ID       *string `json:"id,omitempty"`
	Name     string  `json:"name"`
	Quantity *string `json:"quantity,omitempty"`
}

// ListItemsResponse wraps a list response.
type ListItemsResponse struct {
	Items []Item `json:"items"`
}

// -----------------------------------------------------------------------------
// Tool input types
// -----------------------------------------------------------------------------

type ListItemsInput struct{}

type UpsertItemInput struct {
	Name     string  `json:"name" jsonschema:"Name of the item"`
	ID       *string `json:"id,omitempty" jsonschema:"ID of the item (optional, if not provided a new item will be created)"`
	Quantity *string `json:"quantity,omitempty" jsonschema:"Quantity of the item (optional)"`
}

type RemoveItemInput struct {
	ID string `json:"id" jsonschema:"ID of the item to remove from the shopping list."`
}

// -----------------------------------------------------------------------------
// Firestore service
// -----------------------------------------------------------------------------

// ShoppingListService encapsulates Firestore operations.
type ShoppingListService struct {
	client     *firestore.Client
	database   string
	collection string
}

// NewShoppingListService initializes a Firestore client and returns the service.
func NewShoppingListService(ctx context.Context, projectID, database, collection string, credentialsPath string) (*ShoppingListService, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	if database == "" {
		return nil, errors.New("database is required")
	}
	if collection == "" {
		return nil, errors.New("collection is required")
	}

	var opts []option.ClientOption
	if credentialsPath != "" {
		if _, err := os.Stat(credentialsPath); err != nil {
			return nil, fmt.Errorf("credentials file: %w", err)
		}
		opts = append(opts, option.WithCredentialsFile(credentialsPath))
	}

	client, err := firestore.NewClientWithDatabase(ctx, projectID, database, opts...)
	if err != nil {
		return nil, fmt.Errorf("create firestore client: %w", err)
	}

	return &ShoppingListService{
		client:     client,
		database:   database,
		collection: collection,
	}, nil
}

// Close releases Firestore resources.
func (s *ShoppingListService) Close() error { return s.client.Close() }

// ListItems returns all items in the collection.
func (s *ShoppingListService) ListItems(ctx context.Context) ([]Item, error) {
	docs, err := s.client.Collection(s.collection).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("retrieve items: %w", err)
	}

	items := make([]Item, 0, len(docs))
	for _, d := range docs {
		var it Item
		if err := d.DataTo(&it); err != nil {
			log.Printf("warn: unmarshal item %q: %v", d.Ref.ID, err)
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

// UpsertItem creates a new item (if ID is empty) or updates an existing one.
func (s *ShoppingListService) UpsertItem(ctx context.Context, input ItemInput) ([]Item, error) {
	now := time.Now().UTC()

	if input.ID == nil || *input.ID == "" {
		// create
		id := uuid.New().String()
		item := Item{
			ID:        id,
			Name:      input.Name,
			Quantity:  input.Quantity,
			CreatedAt: now,
		}
		_, err := s.client.Collection(s.collection).Doc(id).Create(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("create item: %w", err)
		}
	} else {
		// update
		updates := []firestore.Update{
			{Path: "name", Value: input.Name},
		}
		if input.Quantity != nil {
			updates = append(updates, firestore.Update{Path: "quantity", Value: *input.Quantity})
		}
		_, err := s.client.Collection(s.collection).Doc(*input.ID).Update(ctx, updates)
		if err != nil {
			return nil, fmt.Errorf("update item: %w", err)
		}
	}

	return s.ListItems(ctx)
}

// RemoveItem deletes a document by ID and returns the remaining list.
func (s *ShoppingListService) RemoveItem(ctx context.Context, id string) ([]Item, error) {
	_, err := s.client.Collection(s.collection).Doc(id).Delete(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete item: %w", err)
	}
	return s.ListItems(ctx)
}

// -----------------------------------------------------------------------------
// MCP server wiring
// -----------------------------------------------------------------------------

func main() {
	// Set the build version from the build info if not set by the build system
	if Version == "dev" || Version == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				Version = bi.Main.Version
			}
		}
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mcp-shopping-list-firestore: ")

	var (
		httpAddr          string
		projectID         string
		credentialsPath   string
		defaultCollection = "shopping"
		showVersion       bool
	)

	flag.StringVar(&httpAddr, "http", "", "run Streaming HTTP transport on the given address, e.g. 8080 (defaults to stdio if empty)")
	flag.StringVar(&credentialsPath, "credentials", "", "path to Google Cloud credentials JSON file (optional; uses default auth if not provided)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		if err := writeVersion(os.Stdout, "mcp-shopping-list-firestore", Version); err != nil {
			fatal("write version: %v", err)
		}
		return
	}

	// Resolve project ID.
	projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")

	if projectID == "" {
		fatal("Google Cloud Project ID is required; set the GOOGLE_CLOUD_PROJECT environment variable")
	}

	// Resolve Firestore database.
	firestoreDatabase := os.Getenv("FIRESTORE_DATABASE")
	if firestoreDatabase == "" {
		fatal("Firestore database name is required; set FIRESTORE_DATABASE")
	}

	ctx := context.Background()

	service, err := NewShoppingListService(ctx, projectID, firestoreDatabase, defaultCollection, credentialsPath)
	if err != nil {
		fatal("initialize Firestore: %v", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			log.Printf("warn: closing Firestore: %v", err)
		}
	}()

	// Create MCP server.
	srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-shopping-list-firestore", Version: Version}, nil)

	// Tools --------------------------------------------------------------------

	// list_items
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_items",
		Description: "Retrieve all items from the shopping list.",
		Title:       "List Shopping Items",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListItemsInput) (
		*mcp.CallToolResult, ListItemsResponse, error,
	) {
		toolCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		items, err := service.ListItems(toolCtx)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("failed to list items: %v", err)}},
				IsError: true,
			}, ListItemsResponse{}, nil
		}
		return nil, ListItemsResponse{Items: items}, nil
	})

	// upsert_item
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "upsert_item",
		Description: "Create a new item or update an existing one. If the item has no id, it's created; otherwise it's updated.",
		Title:       "Upsert Shopping Item",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input UpsertItemInput) (
		*mcp.CallToolResult, ListItemsResponse, error,
	) {
		if input.Name == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "'name' is required"}},
				IsError: true,
			}, ListItemsResponse{}, nil
		}

		toolCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		items, err := service.UpsertItem(toolCtx, ItemInput{
			ID:       input.ID,
			Name:     input.Name,
			Quantity: input.Quantity,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("failed to upsert item: %v", err)}},
				IsError: true,
			}, ListItemsResponse{}, nil
		}
		return nil, ListItemsResponse{Items: items}, nil
	})

	// remove_item
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "remove_item",
		Description: "Remove an item from the shopping list by its ID.",
		Title:       "Remove Shopping Item",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input RemoveItemInput) (
		*mcp.CallToolResult, ListItemsResponse, error,
	) {
		if input.ID == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "invalid or missing 'id'"}},
				IsError: true,
			}, ListItemsResponse{}, nil
		}

		toolCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		items, err := service.RemoveItem(toolCtx, input.ID)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("failed to remove item: %v", err)}},
				IsError: true,
			}, ListItemsResponse{}, nil
		}
		return nil, ListItemsResponse{Items: items}, nil
	})

	// Transport ----------------------------------------------------------------

	if httpAddr != "" {
		fmt.Printf("Starting MCP server using Streamable HTTP transport on %s\n", httpAddr)
		fmt.Printf("Project: %s | Database: %s | Collection: %s\n", projectID, firestoreDatabase, defaultCollection)

		handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
			return srv
		}, &mcp.StreamableHTTPOptions{})

		fmt.Printf("Streamable HTTP Endpoint: http://localhost:%s/mcp\n", httpAddr)

		// Start the server
		if err := http.ListenAndServe(":"+httpAddr, handler); err != nil {
			fatal("Streamable HTTP server failed to start: %v", err)
		}
		return
	}

	// stdio mode
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fatal("MCP stdio terminated: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", a...)
	os.Exit(1)
}
