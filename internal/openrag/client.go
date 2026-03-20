// Package openrag provides a client for the OpenRAG search API.
package openrag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Result represents a single search result from OpenRAG.
type Result struct {
	// Filename is the name of the source document.
	Filename string `json:"filename"`
	// Text is the relevant text excerpt from the document.
	Text string `json:"text"`
	// Relevance is the similarity score between the query and this result (0–1).
	Relevance float64 `json:"relevance"`
}

// Client is an OpenRAG API client.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new OpenRAG client with the given base URL and API key.
// baseURL should not include a trailing slash.
// apiKey is sent via the x-api-key header on every request.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// searchRequest is the JSON body sent to the /search endpoint.
type searchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// searchResponse is the JSON body returned by the /search endpoint.
type searchResponse struct {
	Results []Result `json:"results"`
}

// Search queries OpenRAG for documents matching the given query.
// limit controls the maximum number of results; if 0, it defaults to 5.
// It returns a slice of Result or an error if the request fails or the
// server returns a non-2xx status code.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 5
	}

	reqBody, err := json.Marshal(searchRequest{Query: query, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("openrag: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/search", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("openrag: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrag: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openrag: unexpected status %d", resp.StatusCode)
	}

	var body searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("openrag: decode response: %w", err)
	}

	return body.Results, nil
}
