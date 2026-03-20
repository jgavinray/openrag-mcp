// Package mcp implements an MCP (Model Context Protocol) server over stdio.
//
// The server speaks JSON-RPC 2.0 over stdin/stdout and exposes a single
// "search" tool that delegates to a caller-supplied [SearchHandler].
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

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

// serve is the internal implementation of Serve; it accepts explicit
// reader/writer arguments to enable testing without real stdio.
func (s *Server) serve(ctx context.Context, in io.Reader, out io.Writer) error {
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
			mcp.DefaultNumber(5),
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

		// Extract limit (optional, default 5).
		limit := 5
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

	stdio := mcpserver.NewStdioServer(mcpSrv)
	return stdio.Listen(ctx, in, out)
}
