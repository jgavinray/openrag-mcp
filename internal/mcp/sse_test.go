package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// TestSSETransport_Initialize spins up an SSE test server and verifies that
// the MCP initialize handshake succeeds over HTTP.
func TestSSETransport_Initialize(t *testing.T) {
	handler := func(_ context.Context, query string, limit int) (string, error) {
		return fmt.Sprintf("query=%s limit=%d", query, limit), nil
	}
	srv := NewServer("herald", "0.1.0", handler)
	mcpSrv := srv.buildMCPServer()

	// Use the library's built-in test server helper.
	testSrv := mcpserver.NewTestServer(mcpSrv)
	defer testSrv.Close()

	// Step 1: Connect to the SSE endpoint and grab the message endpoint URL
	// from the first event.
	sseURL := testSrv.URL + "/sse"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sseURL, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := testSrv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect to SSE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}

	// Read events until we find the endpoint event.
	msgEndpoint := ""
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			// The endpoint event carries the message URL.
			if strings.Contains(data, "/message") {
				msgEndpoint = data
				break
			}
		}
	}
	if msgEndpoint == "" {
		t.Fatal("did not receive message endpoint from SSE stream")
	}

	// Step 2: Send an initialize request to the message endpoint.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	}
	body, _ := json.Marshal(initReq)
	postResp, err := testSrv.Client().Post(msgEndpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST initialize: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted && postResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(postResp.Body)
		t.Fatalf("initialize POST status = %d, body = %s", postResp.StatusCode, respBody)
	}

	// Step 3: Read the initialize response from the SSE stream.
	var initResult struct {
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
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(data), &initResult); err == nil && initResult.ID == 1 {
				break
			}
		}
	}

	if initResult.Result.ServerInfo.Name != "herald" {
		t.Errorf("serverInfo.name = %q, want %q", initResult.Result.ServerInfo.Name, "herald")
	}
	if initResult.Result.ProtocolVersion == "" {
		t.Error("protocolVersion is empty")
	}
}

// TestSSETransport_HealthEndpoint verifies the SSE endpoint returns a proper
// Content-Type header (text/event-stream), confirming the HTTP server is up.
func TestSSETransport_HealthEndpoint(t *testing.T) {
	handler := func(_ context.Context, query string, limit int) (string, error) {
		return "ok", nil
	}
	srv := NewServer("herald", "0.1.0", handler)
	mcpSrv := srv.buildMCPServer()

	testSrv := mcpserver.NewTestServer(mcpSrv)
	defer testSrv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, testSrv.URL+"/sse", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := testSrv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /sse: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}
