package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jgavinray/openrag-mcp/internal/mcp"
	"github.com/jgavinray/openrag-mcp/internal/openrag"
)

// rpcMsg is a minimal JSON-RPC 2.0 envelope used in tests.
type rpcMsg struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResp is a minimal JSON-RPC 2.0 response envelope.
type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func ptr(n int) *int { return &n }

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// startMockOpenRAG launches an httptest.Server that returns a fixed JSON
// payload mimicking the OpenRAG /search endpoint.
func startMockOpenRAG(t *testing.T, results []map[string]any) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"results": results}); err != nil {
			t.Errorf("mock encode: %v", err)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// runE2E wires up an MCP server backed by the provided OpenRAG URL/key,
// sends the given newline-separated JSON-RPC messages and returns all
// response lines.
func runE2E(t *testing.T, openragURL, apiKey string, messages []string) []string {
	t.Helper()

	ragClient := openrag.NewClient(openragURL, apiKey)

	searchHandler := func(ctx context.Context, query string, limit int) (string, error) {
		results, err := ragClient.Search(ctx, query, limit)
		if err != nil {
			return "", fmt.Errorf("search failed: %w", err)
		}
		return formatResults(results), nil
	}

	srv := mcp.NewServer("herald", "0.1.0", searchHandler)

	var buf bytes.Buffer
	for _, m := range messages {
		buf.WriteString(m)
		buf.WriteByte('\n')
	}
	in := strings.NewReader(buf.String())
	pr, pw := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		// Use the unexported serve method via the exported Serve wrapper.
		// Since serve is unexported, drive via Serve which reads os.Stdin —
		// instead we call the exported helper that the test package can reach
		// by being in the same package (package main).
		done <- serveWith(ctx, srv, in, pw)
		pw.Close()
	}()

	var lines []string
	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	<-done
	return lines
}

// serveWith is a test shim that calls the internal serve method on the
// MCP server. It lives in package main so it can access mcp.Server's
// exported Serve method, but we need to reach the internal one.
// Instead, we just drive the exported Serve via a context that closes
// stdin after messages are sent — here we call srv.ServeIO which we
// expose only for tests.
//
// Actually: mcp.Server exposes Serve(ctx) which reads from os.Stdin.
// To test with a custom reader/writer we need access to the internal serve
// method. Since cmd/herald is in package main and internal/mcp is a
// separate package, we can't access unexported methods directly.
//
// Solution: expose a package-level helper in internal/mcp that calls serve.
// But we can't change internal/mcp here — instead we replicate the logic
// by building the server manually using the exported API.
func serveWith(ctx context.Context, srv *mcp.Server, in io.Reader, out io.Writer) error {
	return srv.ServeIO(ctx, in, out)
}

// TestE2ESearchReturnsResults is the end-to-end integration test.
// It starts a mock OpenRAG HTTP server, wires it to an MCP server,
// sends initialize + tools/call search, and verifies the formatted output.
func TestE2ESearchReturnsResults(t *testing.T) {
	mockResults := []map[string]any{
		{"filename": "notes.md", "text": "Go is an open source programming language.", "relevance": 0.95},
		{"filename": "guide.md", "text": "MCP enables tool calling.", "relevance": 0.87},
	}
	ts := startMockOpenRAG(t, mockResults)

	initMsg := mustMarshal(rpcMsg{
		JSONRPC: "2.0",
		ID:      ptr(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})
	notif := mustMarshal(rpcMsg{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	callMsg := mustMarshal(rpcMsg{
		JSONRPC: "2.0",
		ID:      ptr(2),
		Method:  "tools/call",
		Params: map[string]any{
			"name": "search",
			"arguments": map[string]any{
				"query": "golang",
				"limit": 5,
			},
		},
	})

	lines := runE2E(t, ts.URL, "test-key", []string{initMsg, notif, callMsg})

	// Find response with id=2 (the tools/call reply).
	var resp rpcResp
	found := false
	for _, line := range lines {
		var r rpcResp
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if r.ID != nil && *r.ID == 2 {
			resp = r
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no response with id=2 found in lines: %v", lines)
	}
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
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.IsError {
		t.Fatalf("isError=true, content: %v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected at least one content item")
	}

	text := result.Content[0].Text

	// Verify format: "Found 2 results:"
	if !strings.Contains(text, "Found 2 results:") {
		t.Errorf("expected 'Found 2 results:' in output, got:\n%s", text)
	}
	// Verify first result filename and relevance.
	if !strings.Contains(text, "notes.md") {
		t.Errorf("expected 'notes.md' in output, got:\n%s", text)
	}
	if !strings.Contains(text, "0.95") {
		t.Errorf("expected relevance '0.95' in output, got:\n%s", text)
	}
	// Verify second result.
	if !strings.Contains(text, "guide.md") {
		t.Errorf("expected 'guide.md' in output, got:\n%s", text)
	}
	if !strings.Contains(text, "0.87") {
		t.Errorf("expected relevance '0.87' in output, got:\n%s", text)
	}
	// Verify text excerpts.
	if !strings.Contains(text, "Go is an open source") {
		t.Errorf("expected text excerpt in output, got:\n%s", text)
	}
}

// TestE2ENoResults verifies that when OpenRAG returns empty results the
// response is "No results found."
func TestE2ENoResults(t *testing.T) {
	ts := startMockOpenRAG(t, []map[string]any{})

	initMsg := mustMarshal(rpcMsg{
		JSONRPC: "2.0",
		ID:      ptr(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	})
	notif := mustMarshal(rpcMsg{JSONRPC: "2.0", Method: "notifications/initialized"})
	callMsg := mustMarshal(rpcMsg{
		JSONRPC: "2.0",
		ID:      ptr(2),
		Method:  "tools/call",
		Params: map[string]any{
			"name":      "search",
			"arguments": map[string]any{"query": "nothing"},
		},
	})

	lines := runE2E(t, ts.URL, "test-key", []string{initMsg, notif, callMsg})

	var resp rpcResp
	for _, line := range lines {
		var r rpcResp
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if r.ID != nil && *r.ID == 2 {
			resp = r
			break
		}
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected isError=true")
	}
	if len(result.Content) == 0 || result.Content[0].Text != "No results found." {
		t.Errorf("expected 'No results found.', got: %v", result.Content)
	}
}

// TestFormatResults verifies the formatResults helper directly.
func TestFormatResults(t *testing.T) {
	results := []openrag.Result{
		{Filename: "alpha.md", Text: "First excerpt.", Relevance: 0.95},
		{Filename: "beta.md", Text: "Second excerpt.", Relevance: 0.80},
	}
	out := formatResults(results)

	if !strings.HasPrefix(out, "Found 2 results:") {
		t.Errorf("expected prefix 'Found 2 results:', got: %q", out)
	}
	if !strings.Contains(out, "1. alpha.md (relevance: 0.95)") {
		t.Errorf("missing first result line, got:\n%s", out)
	}
	if !strings.Contains(out, "2. beta.md (relevance: 0.80)") {
		t.Errorf("missing second result line, got:\n%s", out)
	}
	if !strings.Contains(out, "First excerpt.") {
		t.Errorf("missing first excerpt, got:\n%s", out)
	}
}

// TestFormatResultsEmpty verifies that an empty results slice returns
// "No results found."
func TestFormatResultsEmpty(t *testing.T) {
	out := formatResults(nil)
	if out != "No results found." {
		t.Errorf("expected 'No results found.', got %q", out)
	}
}

// TestLoadConfig verifies that loadConfig returns errors when env vars are missing.
func TestLoadConfig(t *testing.T) {
	// Clear any existing env vars.
	t.Setenv("OPENRAG_URL", "")
	t.Setenv("OPENRAG_API_KEY", "")
	t.Setenv("HERALD_TRANSPORT", "")
	t.Setenv("HERALD_PORT", "")
	t.Setenv("HERALD_ADDR", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error when OPENRAG_URL is missing")
	}

	t.Setenv("OPENRAG_URL", "http://localhost:3000")
	_, err = loadConfig()
	if err == nil {
		t.Fatal("expected error when OPENRAG_API_KEY is missing")
	}

	t.Setenv("OPENRAG_API_KEY", "secret")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.openragURL != "http://localhost:3000" {
		t.Errorf("openragURL = %q, want %q", cfg.openragURL, "http://localhost:3000")
	}
	if cfg.openragAPIKey != "secret" {
		t.Errorf("openragAPIKey = %q, want %q", cfg.openragAPIKey, "secret")
	}
	// Default transport should be stdio.
	if cfg.transport != "stdio" {
		t.Errorf("transport = %q, want %q", cfg.transport, "stdio")
	}
	// Default port should be 8080.
	if cfg.port != "8080" {
		t.Errorf("port = %q, want %q", cfg.port, "8080")
	}
	// Default addr should be 0.0.0.0.
	if cfg.addr != "0.0.0.0" {
		t.Errorf("addr = %q, want %q", cfg.addr, "0.0.0.0")
	}
}

// TestLoadConfigHTTPTransport verifies HTTP transport env vars are loaded correctly.
func TestLoadConfigHTTPTransport(t *testing.T) {
	t.Setenv("OPENRAG_URL", "http://localhost:3000")
	t.Setenv("OPENRAG_API_KEY", "secret")
	t.Setenv("HERALD_TRANSPORT", "http")
	t.Setenv("HERALD_PORT", "9090")
	t.Setenv("HERALD_ADDR", "127.0.0.1")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "http" {
		t.Errorf("transport = %q, want %q", cfg.transport, "http")
	}
	if cfg.port != "9090" {
		t.Errorf("port = %q, want %q", cfg.port, "9090")
	}
	if cfg.addr != "127.0.0.1" {
		t.Errorf("addr = %q, want %q", cfg.addr, "127.0.0.1")
	}
}

// TestLoadConfigInvalidTransport verifies that an unknown transport value
// returns an error.
func TestLoadConfigInvalidTransport(t *testing.T) {
	t.Setenv("OPENRAG_URL", "http://localhost:3000")
	t.Setenv("OPENRAG_API_KEY", "secret")
	t.Setenv("HERALD_TRANSPORT", "grpc")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid HERALD_TRANSPORT")
	}
}
