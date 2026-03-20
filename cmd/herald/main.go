// Command herald is an MCP server that wraps OpenRAG search.
//
// Configuration is read from environment variables:
//
//	OPENRAG_BASE_URL  Base URL of the OpenRAG API (required)
//	OPENRAG_API_KEY   API key for OpenRAG (required)
//	LISTEN_PORT       Port for optional metrics endpoint (default: 8001)
//	LOG_LEVEL         Log level: debug, info, warn, error (default: info)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
	openragBaseURL string
	openragAPIKey  string
	listenPort     string
	logLevel       string
}

// loadConfig reads and validates configuration from environment variables.
// It returns an error if any required variable is missing.
func loadConfig() (config, error) {
	cfg := config{
		openragBaseURL: os.Getenv("OPENRAG_BASE_URL"),
		openragAPIKey:  os.Getenv("OPENRAG_API_KEY"),
		listenPort:     os.Getenv("LISTEN_PORT"),
		logLevel:       os.Getenv("LOG_LEVEL"),
	}

	if cfg.openragBaseURL == "" {
		return config{}, fmt.Errorf("OPENRAG_BASE_URL is required")
	}
	// Strip trailing slash for consistency.
	cfg.openragBaseURL = strings.TrimRight(cfg.openragBaseURL, "/")

	if cfg.listenPort == "" {
		cfg.listenPort = "8001"
	}
	if cfg.logLevel == "" {
		cfg.logLevel = "info"
	}

	return cfg, nil
}

// newLogger creates a structured slog logger at the specified level.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

// validateConnectivity performs a health-check GET against the OpenRAG base URL.
// It is a best-effort check; a non-2xx status or network error is logged as a
// warning but does not prevent startup (the service may not expose a / route).
func validateConnectivity(ctx context.Context, log *slog.Logger, baseURL string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		log.Warn("connectivity check: could not build request", "error", err)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warn("connectivity check: OpenRAG unreachable — will continue anyway", "url", baseURL, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Info("connectivity check: OpenRAG is reachable", "url", baseURL, "status", resp.StatusCode)
	} else {
		log.Warn("connectivity check: unexpected status", "url", baseURL, "status", resp.StatusCode)
	}
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		// Logger may not be initialised yet; write directly to stderr.
		fmt.Fprintf(os.Stderr, "herald: configuration error: %v\n", err)
		os.Exit(1)
	}

	log := newLogger(cfg.logLevel)
	log.Info("herald starting",
		"openrag_base_url", cfg.openragBaseURL,
		"listen_port", cfg.listenPort,
		"log_level", cfg.logLevel,
	)

	// Root context — cancelled on SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Best-effort connectivity validation.
	validateConnectivity(ctx, log, cfg.openragBaseURL)

	// Initialise OpenRAG client.
	ragClient := openrag.NewClient(cfg.openragBaseURL, cfg.openragAPIKey)

	// Build the SearchHandler that bridges the MCP layer to the OpenRAG client.
	searchHandler := func(ctx context.Context, query string, limit int) (string, error) {
		log.Debug("search", "query", query, "limit", limit)
		results, err := ragClient.Search(ctx, query, limit)
		if err != nil {
			log.Error("search failed", "query", query, "error", err)
			return "", fmt.Errorf("search failed: %w", err)
		}

		if len(results) == 0 {
			return "No results found.", nil
		}

		var sb strings.Builder
		for i, r := range results {
			fmt.Fprintf(&sb, "[%d] %s (relevance: %.3f)\n%s\n\n", i+1, r.Filename, r.Relevance, r.Text)
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	}

	// Initialise MCP server.
	srv := mcp.NewServer("herald", "0.1.0", searchHandler)

	// Start MCP server in a goroutine so we can handle shutdown signals.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("MCP server listening on stdio")
		serveErr <- srv.Serve(ctx)
	}()

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
