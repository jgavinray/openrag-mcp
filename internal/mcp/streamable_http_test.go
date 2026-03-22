package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// newStreamableTestServer creates an httptest.Server backed by the Streamable HTTP transport.
func newStreamableTestServer(t *testing.T, handler SearchHandler) *httptest.Server {
	t.Helper()
	srv := NewServer("herald", "0.1.0", handler)
	mcpSrv := srv.buildMCPServer()
	streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv,
		mcpserver.WithEndpointPath("/mcp"),
		mcpserver.WithStateLess(true),
	)
	ts := httptest.NewServer(streamSrv)
	t.Cleanup(ts.Close)
	return ts
}

// postMCP sends a JSON-RPC request to /mcp and returns the response body.
func postMCP(t *testing.T, client *http.Client, baseURL string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	return resp
}

// TestStreamableHTTP_Initialize verifies the MCP initialize handshake over Streamable HTTP.
func TestStreamableHTTP_Initialize(t *testing.T) {
	handler := func(_ context.Context, query string, limit int) (string, error) {
		return fmt.Sprintf("query=%s limit=%d", query, limit), nil
	}
	ts := newStreamableTestServer(t, handler)

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	}

	resp := postMCP(t, ts.Client(), ts.URL, initReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("initialize status = %d, body = %s", resp.StatusCode, body)
	}

	// Response may be JSON or SSE; handle both.
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	ct := resp.Header.Get("Content-Type")
	var jsonData []byte
	if strings.Contains(ct, "text/event-stream") {
		// Extract first data: line from SSE stream.
		for _, line := range strings.Split(string(rawBody), "\n") {
			if strings.HasPrefix(line, "data: ") {
				jsonData = []byte(strings.TrimPrefix(line, "data: "))
				break
			}
		}
	} else {
		jsonData = rawBody
	}

	var result struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(jsonData, &result); err != nil {
		t.Fatalf("unmarshal initialize response: %v (raw: %s)", err, jsonData)
	}

	if result.Result.ServerInfo.Name != "herald" {
		t.Errorf("serverInfo.name = %q, want %q", result.Result.ServerInfo.Name, "herald")
	}
	if result.Result.ProtocolVersion == "" {
		t.Error("protocolVersion is empty")
	}
}

// TestStreamableHTTP_EndpointResponds verifies the /mcp endpoint is reachable and
// rejects malformed requests with a 4xx status.
func TestStreamableHTTP_EndpointResponds(t *testing.T) {
	handler := func(_ context.Context, query string, limit int) (string, error) {
		return "ok", nil
	}
	ts := newStreamableTestServer(t, handler)

	// POST with invalid JSON should return 4xx.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp",
		strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 {
		t.Errorf("expected 4xx for invalid JSON, got %d", resp.StatusCode)
	}
}
