// Command herald is an MCP server that wraps OpenRAG search.
//
// Configuration is read from environment variables:
//
//	OPENRAG_URL      Base URL of the OpenRAG API (required), e.g. http://192.168.0.44:3000
//	OPENRAG_API_KEY  API key for OpenRAG (required)
//	HERALD_TRANSPORT Transport mode: "stdio" (default) or "http"
//	HERALD_PORT      Port for HTTP mode (default "8080")
//	HERALD_ADDR      Bind address for HTTP mode (default "0.0.0.0")
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jgavinray/openrag-mcp/internal/mcp"
	"github.com/jgavinray/openrag-mcp/internal/openrag"
)

// config holds runtime configuration loaded from environment variables.
type config struct {
	openragURL    string
	openragAPIKey string
	transport     string // "stdio" or "http"
	port          string // port for HTTP mode
	addr          string // bind address for HTTP mode
}

// loadConfig reads and validates configuration from environment variables.
// It returns an error if any required variable is missing.
func loadConfig() (config, error) {
	transport := os.Getenv("HERALD_TRANSPORT")
	if transport == "" {
		transport = "stdio"
	}
	port := os.Getenv("HERALD_PORT")
	if port == "" {
		port = "8080"
	}
	bindAddr := os.Getenv("HERALD_ADDR")
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}

	cfg := config{
		openragURL:    os.Getenv("OPENRAG_URL"),
		openragAPIKey: os.Getenv("OPENRAG_API_KEY"),
		transport:     transport,
		port:          port,
		addr:          bindAddr,
	}

	if cfg.openragURL == "" {
		return config{}, fmt.Errorf("OPENRAG_URL is required")
	}
	// Strip trailing slash for consistency.
	cfg.openragURL = strings.TrimRight(cfg.openragURL, "/")

	if cfg.openragAPIKey == "" {
		return config{}, fmt.Errorf("OPENRAG_API_KEY is required")
	}

	switch cfg.transport {
	case "stdio", "http":
		// valid
	default:
		return config{}, fmt.Errorf("HERALD_TRANSPORT must be \"stdio\" or \"http\", got %q", cfg.transport)
	}

	return cfg, nil
}

// formatResults formats a slice of openrag.Result into a human-readable string.
// The format is:
//
//	Found N results:
//
//	1. filename.md (relevance: 0.95)
//	   Text excerpt here...
//
//	2. another.md (relevance: 0.87)
//	   ...
func formatResults(results []openrag.Result) string {
	if len(results) == 0 {
		return "No results found."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d results:\n", len(results))
	for i, r := range results {
		fmt.Fprintf(&sb, "\n%d. %s (relevance: %.2f)\n   %s\n", i+1, r.Filename, r.Relevance, r.Text)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "herald: configuration error: %v\n", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	log.Info("herald starting", "openrag_url", cfg.openragURL)

	// Root context — cancelled on SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Initialise OpenRAG client.
	ragClient := openrag.NewClient(cfg.openragURL, cfg.openragAPIKey)

	// Build the SearchHandler that bridges the MCP layer to the OpenRAG client.
	searchHandler := func(ctx context.Context, query string, limit int) (string, error) {
		log.Debug("search", "query", query, "limit", limit)
		results, err := ragClient.Search(ctx, query, limit)
		if err != nil {
			log.Error("search failed", "query", query, "error", err)
			return "", fmt.Errorf("search failed: %w", err)
		}
		return formatResults(results), nil
	}

	// Initialise MCP server.
	srv := mcp.NewServer("herald", "0.1.0", searchHandler)

	// Start MCP server in a goroutine so we can handle shutdown signals.
	serveErr := make(chan error, 1)
	switch cfg.transport {
	case "http":
		listenAddr := cfg.addr + ":" + cfg.port
		baseURL := "http://" + listenAddr
		log.Info("MCP server listening via HTTP/SSE", "addr", listenAddr)
		go func() {
			serveErr <- srv.ServeSSE(ctx, listenAddr, baseURL)
		}()
	default:
		log.Info("MCP server listening on stdio")
		go func() {
			serveErr <- srv.Serve(ctx)
		}()
	}

	select {
	case err := <-serveErr:
		if err != nil && err != context.Canceled {
			log.Error("MCP server exited with error", "error", err)
			os.Exit(1)
		}
		log.Info("MCP server stopped")
	case <-ctx.Done():
		log.Info("shutdown signal received, stopping herald")
		// Wait briefly for the server goroutine to drain.
		select {
		case <-serveErr:
		case <-time.After(5 * time.Second):
			log.Warn("graceful shutdown timed out")
		}
	}

	log.Info("herald exited cleanly")
}
