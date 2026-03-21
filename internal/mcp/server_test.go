package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// rpcRequest is a minimal JSON-RPC 2.0 request envelope used in tests.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"` // nil for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is a minimal JSON-RPC 2.0 response envelope used to decode
// server output in tests.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// id is a helper that returns a pointer to an int, for use in rpcRequest.
func id(n int) *int { return &n }

// newTestServer returns a Server wired with a simple echo handler for tests.
func newTestServer() *Server {
	handler := func(_ context.Context, query string, limit int) (string, error) {
		return fmt.Sprintf("query=%s limit=%d", query, limit), nil
	}
	return NewServer("openrag-mcp", "0.1.0", handler)
}

// runSession sends a sequence of newline-terminated JSON-RPC messages to srv,
// closes the write end of the pipe, and returns all response lines.
// It cancels the server after a short timeout to avoid hanging.
func runSession(t *testing.T, srv *Server, messages []string) []string {
	t.Helper()

	// Build combined input: each message on its own line.
	var buf bytes.Buffer
	for _, m := range messages {
		buf.WriteString(m)
		buf.WriteByte('\n')
	}

	in := strings.NewReader(buf.String())
	pr, pw := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.serve(ctx, in, pw)
		pw.Close()
	}()

	// Read all lines from the response pipe.
	var lines []string
	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	<-done
	return lines
}

// marshal returns the JSON encoding of v (panics on error; tests only).
func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// parseResponse decodes the first JSON-RPC response from lines whose
// id field matches wantID.
func parseResponse(t *testing.T, lines []string, wantID int) rpcResponse {
	t.Helper()
	for _, line := range lines {
		var resp rpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		if resp.ID != nil && *resp.ID == wantID {
			return resp
		}
	}
	t.Fatalf("no response with id=%d found in: %v", wantID, lines)
	return rpcResponse{}
}

// --- Tests ---

// TestInitializeHandshake verifies that the server responds to an
// initialize request with protocol version and capabilities.
func TestInitializeHandshake(t *testing.T) {
	srv := newTestServer()

	initMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})

	lines := runSession(t, srv, []string{initMsg})

	resp := parseResponse(t, lines, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if result.ServerInfo.Name != "openrag-mcp" {
		t.Errorf("serverInfo.name = %q, want %q", result.ServerInfo.Name, "openrag-mcp")
	}
	if result.ProtocolVersion == "" {
		t.Error("protocolVersion is empty")
	}
}

// TestToolsList verifies that tools/list returns the search tool with the
// correct schema (query required, limit optional).
func TestToolsList(t *testing.T) {
	srv := newTestServer()

	initMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})
	notif := marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	listMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(2),
		Method:  "tools/list",
	})

	lines := runSession(t, srv, []string{initMsg, notif, listMsg})

	resp := parseResponse(t, lines, 2)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal tools/list result: %v", err)
	}

	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	tool := result.Tools[0]
	if tool.Name != "search" {
		t.Errorf("tool name = %q, want %q", tool.Name, "search")
	}
	if tool.Description == "" {
		t.Error("tool description is empty")
	}
	if _, ok := tool.InputSchema.Properties["query"]; !ok {
		t.Error("missing property: query")
	}
	if _, ok := tool.InputSchema.Properties["limit"]; !ok {
		t.Error("missing property: limit")
	}
	found := false
	for _, r := range tool.InputSchema.Required {
		if r == "query" {
			found = true
		}
	}
	if !found {
		t.Errorf("query is not in required list: %v", tool.InputSchema.Required)
	}
}

// TestToolsCallDispatch verifies that tools/call dispatches to the handler
// with the correct query and limit arguments.
func TestToolsCallDispatch(t *testing.T) {
	var gotQuery string
	var gotLimit int

	srv := NewServer("openrag-mcp", "0.1.0", func(_ context.Context, query string, limit int) (string, error) {
		gotQuery = query
		gotLimit = limit
		return "ok", nil
	})

	initMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})
	notif := marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	callMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(2),
		Method:  "tools/call",
		Params: map[string]any{
			"name": "search",
			"arguments": map[string]any{
				"query": "golang MCP",
				"limit": 10,
			},
		},
	})

	lines := runSession(t, srv, []string{initMsg, notif, callMsg})

	resp := parseResponse(t, lines, 2)
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal call result: %v", err)
	}
	if result.IsError {
		t.Fatalf("isError=true, content: %v", result.Content)
	}
	if gotQuery != "golang MCP" {
		t.Errorf("handler received query=%q, want %q", gotQuery, "golang MCP")
	}
	if gotLimit != 10 {
		t.Errorf("handler received limit=%d, want 10", gotLimit)
	}
}

// TestToolsCallMissingQuery verifies that tools/call returns an error when
// the required "query" parameter is absent.
func TestToolsCallMissingQuery(t *testing.T) {
	srv := newTestServer()

	initMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})
	notif := marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	callMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(2),
		Method:  "tools/call",
		Params: map[string]any{
			"name":      "search",
			"arguments": map[string]any{
				// query intentionally omitted
			},
		},
	})

	lines := runSession(t, srv, []string{initMsg, notif, callMsg})

	resp := parseResponse(t, lines, 2)

	// The MCP spec says tool errors should be returned as isError:true in the
	// result, not as a JSON-RPC protocol error.
	if resp.Error != nil {
		// Accept a JSON-RPC error too — either way the server must not crash.
		return
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal call result: %v", err)
	}
	if !result.IsError {
		t.Error("expected isError=true when required parameter query is missing")
	}
}

// TestUnknownMethodReturnsError verifies that sending an unrecognised
// JSON-RPC method produces a JSON-RPC error response (not a crash).
func TestUnknownMethodReturnsError(t *testing.T) {
	srv := newTestServer()

	initMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})
	unknownMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(2),
		Method:  "no/such/method",
	})

	lines := runSession(t, srv, []string{initMsg, unknownMsg})

	resp := parseResponse(t, lines, 2)
	if resp.Error == nil {
		t.Error("expected JSON-RPC error for unknown method, got none")
	}
	if resp.Error != nil && resp.Error.Code == 0 {
		t.Errorf("error code should be non-zero, got %d", resp.Error.Code)
	}
}

// TestDefaultSearchLimit verifies that the exported DefaultSearchLimit
// constant is applied when the optional "limit" parameter is omitted.
func TestDefaultSearchLimit(t *testing.T) {
	var gotLimit int
	srv := NewServer("openrag-mcp", "0.1.0", func(_ context.Context, query string, limit int) (string, error) {
		gotLimit = limit
		return "ok", nil
	})

	initMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})
	notif := marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	// Call without specifying limit — handler should receive DefaultSearchLimit.
	callMsg := marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id(2),
		Method:  "tools/call",
		Params: map[string]any{
			"name": "search",
			"arguments": map[string]any{
				"query": "default limit test",
			},
		},
	})

	lines := runSession(t, srv, []string{initMsg, notif, callMsg})

	resp := parseResponse(t, lines, 2)
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}

	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal call result: %v", err)
	}
	if result.IsError {
		t.Fatal("unexpected isError=true")
	}
	if gotLimit != DefaultSearchLimit {
		t.Errorf("handler received limit=%d, want DefaultSearchLimit=%d", gotLimit, DefaultSearchLimit)
	}
}
