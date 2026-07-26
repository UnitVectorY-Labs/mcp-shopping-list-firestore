package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestVersionVariableIsNotEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("expected non-empty default version")
	}
}

func TestFormatVersionAddsVPrefixAndMetadata(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "1.2.3")
	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3 (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestFormatVersionPreservesExistingVPrefix(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "v1.2.3")
	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3 (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestFormatVersionAddsVPrefixToPrerelease(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "1.2.3-rc1")
	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3-rc1 (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestFormatVersionPreservesDevVersion(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "dev")
	want := fmt.Sprintf("mcp-shopping-list-firestore version dev (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestWriteVersionWritesTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	if err := writeVersion(&buf, "mcp-shopping-list-firestore", "1.2.3"); err != nil {
		t.Fatalf("writeVersion returned error: %v", err)
	}
	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3 (%s, %s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if buf.String() != want {
		t.Fatalf("unexpected written version output: got %q, want %q", buf.String(), want)
	}
}

// -----------------------------------------------------------------------------
// Stub service for MCP server tests (no Firestore dependency)
// -----------------------------------------------------------------------------

type stubItemService struct {
	items []Item
}

func (s *stubItemService) ListItems(_ context.Context) ([]Item, error) {
	return s.items, nil
}

func (s *stubItemService) UpsertItem(_ context.Context, input ItemInput) ([]Item, error) {
	if input.ID == nil || *input.ID == "" {
		item := Item{
			ID:        "stub-id",
			Name:      input.Name,
			Quantity:  input.Quantity,
			CreatedAt: time.Now().UTC(),
		}
		s.items = append(s.items, item)
	} else {
		for i, it := range s.items {
			if it.ID == *input.ID {
				s.items[i].Name = input.Name
				if input.Quantity != nil {
					s.items[i].Quantity = input.Quantity
				}
				break
			}
		}
	}
	return s.items, nil
}

func (s *stubItemService) RemoveItem(_ context.Context, id string) ([]Item, error) {
	filtered := s.items[:0]
	for _, it := range s.items {
		if it.ID != id {
			filtered = append(filtered, it)
		}
	}
	s.items = filtered
	return s.items, nil
}

// -----------------------------------------------------------------------------
// Helper: connect an MCP client to the server via in-memory transport
// -----------------------------------------------------------------------------

func connectTestClient(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { clientSession.Close() })

	return clientSession
}

// schemaRawMessage converts an any InputSchema value to json.RawMessage.
func schemaRawMessage(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	return b
}

// -----------------------------------------------------------------------------
// MCP protocol tests
// -----------------------------------------------------------------------------

func TestMCPServerReturnsCorrectServerInfo(t *testing.T) {
	srv := buildMCPServer("v1.2.3", &stubItemService{})
	cs := connectTestClient(t, srv)

	initResult := cs.InitializeResult()
	if initResult == nil || initResult.ServerInfo == nil {
		t.Fatal("InitializeResult or ServerInfo is nil")
	}
	if initResult.ServerInfo.Name != "mcp-shopping-list-firestore" {
		t.Errorf("server name: got %q, want %q", initResult.ServerInfo.Name, "mcp-shopping-list-firestore")
	}
	if initResult.ServerInfo.Version != "v1.2.3" {
		t.Errorf("server version: got %q, want %q", initResult.ServerInfo.Version, "v1.2.3")
	}
}

func TestMCPServerListsThreeTools(t *testing.T) {
	srv := buildMCPServer("dev", &stubItemService{})
	cs := connectTestClient(t, srv)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(res.Tools))
	}

	names := make([]string, len(res.Tools))
	for i, tool := range res.Tools {
		names[i] = tool.Name
	}
	sort.Strings(names)

	want := []string{"list_items", "remove_item", "upsert_item"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("tool[%d]: got %q, want %q", i, names[i], w)
		}
	}
}

func TestMCPToolAnnotations(t *testing.T) {
	srv := buildMCPServer("dev", &stubItemService{})
	cs := connectTestClient(t, srv)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	toolByName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		toolByName[tool.Name] = tool
	}

	// list_items: readOnlyHint = true, correct title
	listTool, ok := toolByName["list_items"]
	if !ok {
		t.Fatal("list_items not found")
	}
	if listTool.Annotations == nil || !listTool.Annotations.ReadOnlyHint {
		t.Error("list_items: expected ReadOnlyHint=true")
	}
	if listTool.Title != "List Shopping Items" {
		t.Errorf("list_items title: got %q, want %q", listTool.Title, "List Shopping Items")
	}

	// upsert_item: correct title
	upsertTool, ok := toolByName["upsert_item"]
	if !ok {
		t.Fatal("upsert_item not found")
	}
	if upsertTool.Title != "Upsert Shopping Item" {
		t.Errorf("upsert_item title: got %q, want %q", upsertTool.Title, "Upsert Shopping Item")
	}

	// remove_item: correct title
	removeTool, ok := toolByName["remove_item"]
	if !ok {
		t.Fatal("remove_item not found")
	}
	if removeTool.Title != "Remove Shopping Item" {
		t.Errorf("remove_item title: got %q, want %q", removeTool.Title, "Remove Shopping Item")
	}
}

func TestMCPToolInputSchemas(t *testing.T) {
	srv := buildMCPServer("dev", &stubItemService{})
	cs := connectTestClient(t, srv)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	toolByName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		toolByName[tool.Name] = tool
	}

	// upsert_item: name is required; id and quantity are optional
	upsertTool := toolByName["upsert_item"]
	if upsertTool == nil {
		t.Fatal("upsert_item not found")
	}
	upsertSchema := schemaRawMessage(t, upsertTool.InputSchema)
	assertSchemaRequired(t, "upsert_item", upsertSchema, []string{"name"})
	assertSchemaHasProperty(t, "upsert_item", upsertSchema, "name")
	assertSchemaHasProperty(t, "upsert_item", upsertSchema, "id")
	assertSchemaHasProperty(t, "upsert_item", upsertSchema, "quantity")

	// remove_item: id is required
	removeTool := toolByName["remove_item"]
	if removeTool == nil {
		t.Fatal("remove_item not found")
	}
	removeSchema := schemaRawMessage(t, removeTool.InputSchema)
	assertSchemaRequired(t, "remove_item", removeSchema, []string{"id"})
	assertSchemaHasProperty(t, "remove_item", removeSchema, "id")

	// list_items: no required properties
	listTool := toolByName["list_items"]
	if listTool == nil {
		t.Fatal("list_items not found")
	}
	listSchema := schemaRawMessage(t, listTool.InputSchema)
	assertSchemaRequired(t, "list_items", listSchema, []string{})
}

func assertSchemaRequired(t *testing.T, toolName string, schema json.RawMessage, wantRequired []string) {
	t.Helper()
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Errorf("%s: unmarshal schema: %v", toolName, err)
		return
	}
	sort.Strings(s.Required)
	sort.Strings(wantRequired)
	if len(s.Required) != len(wantRequired) {
		t.Errorf("%s: required fields: got %v, want %v", toolName, s.Required, wantRequired)
		return
	}
	for i := range wantRequired {
		if s.Required[i] != wantRequired[i] {
			t.Errorf("%s: required[%d]: got %q, want %q", toolName, i, s.Required[i], wantRequired[i])
		}
	}
}

func assertSchemaHasProperty(t *testing.T, toolName string, schema json.RawMessage, prop string) {
	t.Helper()
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Errorf("%s: unmarshal schema: %v", toolName, err)
		return
	}
	if _, ok := s.Properties[prop]; !ok {
		t.Errorf("%s: missing property %q in input schema", toolName, prop)
	}
}

func TestMCPUpsertItemMissingNameValidation(t *testing.T) {
	srv := buildMCPServer("dev", &stubItemService{})
	cs := connectTestClient(t, srv)

	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "upsert_item",
		Arguments: map[string]any{"id": "some-id"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError=true for missing required 'name'")
	}
	if len(res.Content) == 0 {
		t.Error("expected non-empty Content in error result")
	}
}

func TestMCPRemoveItemMissingIDValidation(t *testing.T) {
	srv := buildMCPServer("dev", &stubItemService{})
	cs := connectTestClient(t, srv)

	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remove_item",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError=true for missing required 'id'")
	}
}

func TestMCPListItemsReturnsItems(t *testing.T) {
	svc := &stubItemService{
		items: []Item{
			{ID: "1", Name: "Milk", CreatedAt: time.Now().UTC()},
			{ID: "2", Name: "Eggs", CreatedAt: time.Now().UTC()},
		},
	}
	srv := buildMCPServer("dev", svc)
	cs := connectTestClient(t, srv)

	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_items",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected non-empty Content")
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}

	var got ListItemsResponse
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(got.Items))
	}
}

func TestMCPUpsertItemCreatesItem(t *testing.T) {
	svc := &stubItemService{}
	srv := buildMCPServer("dev", svc)
	cs := connectTestClient(t, srv)

	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "upsert_item",
		Arguments: map[string]any{"name": "Bread"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	var got ListItemsResponse
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Items) != 1 {
		t.Errorf("expected 1 item after upsert, got %d", len(got.Items))
	}
	if got.Items[0].Name != "Bread" {
		t.Errorf("item name: got %q, want %q", got.Items[0].Name, "Bread")
	}
}

func TestMCPRemoveItemDeletesItem(t *testing.T) {
	svc := &stubItemService{
		items: []Item{
			{ID: "abc-123", Name: "Butter", CreatedAt: time.Now().UTC()},
		},
	}
	srv := buildMCPServer("dev", svc)
	cs := connectTestClient(t, srv)

	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remove_item",
		Arguments: map[string]any{"id": "abc-123"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	var got ListItemsResponse
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("expected 0 items after remove, got %d", len(got.Items))
	}
}

func TestMCPUpsertItemEmptyNameRejected(t *testing.T) {
	srv := buildMCPServer("dev", &stubItemService{})
	cs := connectTestClient(t, srv)

	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "upsert_item",
		Arguments: map[string]any{"name": ""},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError=true for empty 'name'")
	}
}

func TestMCPRemoveItemEmptyIDRejected(t *testing.T) {
	srv := buildMCPServer("dev", &stubItemService{})
	cs := connectTestClient(t, srv)

	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remove_item",
		Arguments: map[string]any{"id": ""},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError=true for empty 'id'")
	}
}
