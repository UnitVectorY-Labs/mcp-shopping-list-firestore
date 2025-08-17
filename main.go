package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/api/option"
)

// Version is set by the build system.
var Version = "dev"

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

// UpsertItemRequest is the tool request for creating/updating a single item.
type UpsertItemRequest struct {
	ID       *string `json:"id,omitempty"`
	Name     string  `json:"name"`
	Quantity *string `json:"quantity,omitempty"`
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
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mcp-shopping-list-firestore: ")

	var (
		httpAddr          string
		projectID         string
		credentialsPath   string
		defaultCollection = "shopping"
	)

	flag.StringVar(&httpAddr, "http", "", "run Streaming HTTP transport on the given address, e.g. 8080 (defaults to stdio if empty)")
	flag.StringVar(&credentialsPath, "credentials", "", "path to Google Cloud credentials JSON file (optional; uses default auth if not provided)")
	flag.Parse()

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
	srv := server.NewMCPServer("mcp-shopping-list-firestore", Version)

	// Tools --------------------------------------------------------------------

	// list_items
	listItemsTool := mcp.NewTool(
		"list_items",
		mcp.WithDescription("Retrieve all items from the shopping list."),
		mcp.WithTitleAnnotation("List Shopping Items"),
		mcp.WithReadOnlyHintAnnotation(true),
	)
	srv.AddTool(listItemsTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		toolCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		items, err := service.ListItems(toolCtx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list items: %v", err)), nil
		}
		return jsonResult(ListItemsResponse{Items: items})
	})

	// upsert_item
	upsertItemTool := mcp.NewTool(
		"upsert_item",
		mcp.WithDescription("Create a new item or update an existing one. If the item has no id, it's created; otherwise it's updated."),
		mcp.WithTitleAnnotation("Upsert Shopping Item"),
		mcp.WithString("name", mcp.Description("Name of the item"), mcp.Required()),
		mcp.WithString("id", mcp.Description("ID of the item (optional, if not provided a new item will be created)")),
		mcp.WithString("quantity", mcp.Description("Quantity of the item (optional)")),
	)
	srv.AddTool(upsertItemTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		var itemReq UpsertItemRequest

		// Extract required name field
		if name, ok := args["name"].(string); ok {
			itemReq.Name = name
		} else {
			return mcp.NewToolResultError("invalid or missing 'name'"), nil
		}

		// Extract optional id field
		if id, ok := args["id"].(string); ok && id != "" {
			itemReq.ID = &id
		}

		// Extract optional quantity field
		if quantity, ok := args["quantity"].(string); ok && quantity != "" {
			itemReq.Quantity = &quantity
		}

		// Validate required fields
		if itemReq.Name == "" {
			return mcp.NewToolResultError("'name' is required"), nil
		}

		toolCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		items, err := service.UpsertItem(toolCtx, ItemInput{
			ID:       itemReq.ID,
			Name:     itemReq.Name,
			Quantity: itemReq.Quantity,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to upsert item: %v", err)), nil
		}
		return jsonResult(ListItemsResponse{Items: items})
	})

	// remove_item
	removeItemTool := mcp.NewTool(
		"remove_item",
		mcp.WithDescription("Remove an item from the shopping list by its ID."),
		mcp.WithTitleAnnotation("Remove Shopping Item"),
		mcp.WithString("id", mcp.Description("ID of the item to remove from the shopping list."), mcp.Required()),
	)
	srv.AddTool(removeItemTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()

		// Extract required id field
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return mcp.NewToolResultError("invalid or missing 'id'"), nil
		}

		toolCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		items, err := service.RemoveItem(toolCtx, id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to remove item: %v", err)), nil
		}
		return jsonResult(ListItemsResponse{Items: items})
	})

	// Transport ----------------------------------------------------------------

	if httpAddr != "" {
		fmt.Printf("Starting MCP server using Streamable HTTP transport on %s\n", httpAddr)
		fmt.Printf("Project: %s | Database: %s | Collection: %s\n", projectID, firestoreDatabase, defaultCollection)

		// Create HTTP server
		httpServer := server.NewStreamableHTTPServer(srv)

		fmt.Printf("Streamable HTTP Endpoint: http://localhost:%s/mcp\n", httpAddr)

		// Start the server
		if err := httpServer.Start(":" + httpAddr); err != nil {
			fatal("Streamable HTTP server failed to start: %v", err)
		}
		return
	}

	// stdio mode
	if err := server.ServeStdio(srv); err != nil {
		fatal("MCP stdio terminated: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", a...)
	os.Exit(1)
}

// jsonResult marshals v as JSON into an MCP text result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encode response: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
