// Package mcp implements an MCP (Model Context Protocol) server.
//
// The server speaks JSON-RPC 2.0 and exposes a single "search" tool that
// delegates to a caller-supplied [SearchHandler]. Two transports are
// supported:
//
//   - stdio: reads from stdin and writes to stdout (default, for local use)
//   - HTTP/SSE: listens on a TCP address, suitable for network deployment
//
// # Quick start (stdio)
//
//	srv := mcp.NewServer("openrag-mcp", "0.1.0", func(ctx context.Context, query string, limit int) (string, error) {
//	    return mySearch(ctx, query, limit)
//	})
//	if err := srv.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
//	    log.Fatal(err)
//	}
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// DefaultSearchLimit is the default maximum number of results returned by the
// search tool when the caller omits the optional "limit" parameter.
const DefaultSearchLimit = 5

// SearchHandler is a function that performs a search over the OpenRAG knowledge
// base. query is the search string and limit is the maximum number of results
// to return. It returns a formatted string of results or an error.
type SearchHandler func(ctx context.Context, query string, limit int) (string, error)

// Server is an MCP server that exposes a single "search" tool.
// Create one with [NewServer] and run it with [Server.Serve].
type Server struct {
	name    string
	version string
	handler SearchHandler
}

// NewServer creates a new MCP server with the given name, version, and search
// handler. name and version are reported in the initialize handshake.
func NewServer(name, version string, handler SearchHandler) *Server {
	return &Server{
		name:    name,
		version: version,
		handler: handler,
	}
}

// Serve starts the MCP server, reading JSON-RPC messages from stdin and
// writing responses to stdout. It blocks until ctx is cancelled or stdin is
// closed.
func (s *Server) Serve(ctx context.Context) error {
	return s.serve(ctx, os.Stdin, os.Stdout)
}

// ServeIO starts the MCP server reading from r and writing to w.
// It is intended for testing; production code should use [Server.Serve].
func (s *Server) ServeIO(ctx context.Context, r io.Reader, w io.Writer) error {
	return s.serve(ctx, r, w)
}

// buildMCPServer creates and configures the underlying MCPServer with all
// tools registered. It is shared by both transport implementations.
func (s *Server) buildMCPServer() *mcpserver.MCPServer {
	mcpSrv := mcpserver.NewMCPServer(
		s.name,
		s.version,
		mcpserver.WithToolCapabilities(true),
	)

	searchTool := mcp.NewTool(
		"search",
		mcp.WithDescription("Search the OpenRAG knowledge base for relevant documents"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The search query"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default 5)"),
			mcp.DefaultNumber(DefaultSearchLimit),
		),
	)

	handler := s.handler
	mcpSrv.AddTool(searchTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()

		// Extract query (required).
		queryVal, ok := args["query"]
		if !ok {
			return nil, fmt.Errorf("missing required parameter: query")
		}
		query, ok := queryVal.(string)
		if !ok || query == "" {
			return nil, fmt.Errorf("parameter query must be a non-empty string")
		}

		// Extract limit (optional, default DefaultSearchLimit).
		limit := DefaultSearchLimit
		if limitVal, exists := args["limit"]; exists && limitVal != nil {
			switch v := limitVal.(type) {
			case float64:
				limit = int(v)
			case json.Number:
				n, err := v.Int64()
				if err != nil {
					return nil, fmt.Errorf("parameter limit must be an integer: %w", err)
				}
				limit = int(n)
			case int:
				limit = v
			case int64:
				limit = int(v)
			}
		}

		result, err := handler(ctx, query, limit)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{Type: "text", Text: err.Error()},
				},
				IsError: true,
			}, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: result},
			},
		}, nil
	})

	return mcpSrv
}

// serve is the internal implementation of Serve; it accepts explicit
// reader/writer arguments to enable testing without real stdio.
func (s *Server) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	mcpSrv := s.buildMCPServer()
	stdio := mcpserver.NewStdioServer(mcpSrv)
	return stdio.Listen(ctx, in, out)
}

// ServeSSE starts an HTTP/SSE MCP server on the given address (e.g.
// "0.0.0.0:8080"). The baseURL must be the publicly reachable URL of the
// server so that SSE clients can construct the message endpoint URL
// (e.g. "http://192.168.1.10:8080"). It blocks until ctx is cancelled or an
// unrecoverable error occurs.
func (s *Server) ServeSSE(ctx context.Context, addr, baseURL string) error {
	mcpSrv := s.buildMCPServer()
	sseSrv := mcpserver.NewSSEServer(mcpSrv,
		mcpserver.WithBaseURL(baseURL),
		mcpserver.WithKeepAlive(true),
	)

	// Start the HTTP listener in a goroutine so we can watch ctx.
	errCh := make(chan error, 1)
	go func() {
		errCh <- sseSrv.Start(addr)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return sseSrv.Shutdown(shutdownCtx)
	}
}
