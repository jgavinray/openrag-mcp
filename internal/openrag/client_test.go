package openrag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestServer creates an httptest.Server that responds with the given status
// code and JSON body. The caller is responsible for calling ts.Close().
func newTestServer(t *testing.T, statusCode int, body interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if body != nil {
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Errorf("test server encode: %v", err)
			}
		}
	}))
}

func TestSearch_Success(t *testing.T) {
	want := []Result{
		{Filename: "doc1.pdf", Text: "hello world", Relevance: 0.95},
		{Filename: "doc2.pdf", Text: "foo bar", Relevance: 0.80},
	}
	ts := newTestServer(t, http.StatusOK, searchResponse{Results: want})
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	got, err := c.Search(context.Background(), "hello", 2)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Search() returned %d results, want %d", len(got), len(want))
	}
	for i, r := range got {
		if r.Filename != want[i].Filename {
			t.Errorf("result[%d].Filename = %q, want %q", i, r.Filename, want[i].Filename)
		}
		if r.Text != want[i].Text {
			t.Errorf("result[%d].Text = %q, want %q", i, r.Text, want[i].Text)
		}
		if r.Relevance != want[i].Relevance {
			t.Errorf("result[%d].Relevance = %v, want %v", i, r.Relevance, want[i].Relevance)
		}
	}
}

func TestSearch_DefaultLimit(t *testing.T) {
	var gotLimit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotLimit = req.Limit
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(searchResponse{}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	if _, err := c.Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if gotLimit != 5 {
		t.Errorf("default limit = %d, want 5", gotLimit)
	}
}

func TestSearch_EmptyResults(t *testing.T) {
	ts := newTestServer(t, http.StatusOK, searchResponse{Results: []Result{}})
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	got, err := c.Search(context.Background(), "nothing", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Search() returned %d results, want 0", len(got))
	}
}

func TestSearch_HTTPError(t *testing.T) {
	ts := newTestServer(t, http.StatusInternalServerError, nil)
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	_, err := c.Search(context.Background(), "query", 5)
	if err == nil {
		t.Fatal("Search() expected error, got nil")
	}
}

func TestSearch_ContextCancellation(t *testing.T) {
	// Server that deliberately hangs until the client disconnects.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := NewClient(ts.URL, "test-key")
	_, err := c.Search(ctx, "query", 5)
	if err == nil {
		t.Fatal("Search() expected error from cancelled context, got nil")
	}
}

func TestSearch_APIKeyHeader(t *testing.T) {
	const wantKey = "super-secret-key"
	var gotKey string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(searchResponse{}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer ts.Close()

	c := NewClient(ts.URL, wantKey)
	if _, err := c.Search(context.Background(), "q", 1); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if gotKey != wantKey {
		t.Errorf("x-api-key header = %q, want %q", gotKey, wantKey)
	}
}
