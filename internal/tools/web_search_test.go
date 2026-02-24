package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type mockSearchProvider struct {
	results []SearchResult
	err     error
}

func (m *mockSearchProvider) Search(_ context.Context, _ string, _ int) ([]SearchResult, error) {
	return m.results, m.err
}

func TestWebSearchNoProvider(t *testing.T) {
	t.Parallel()
	tool := NewWebSearch("")
	args, _ := json.Marshal(webSearchArgs{Query: "test"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var s string
	if err := json.Unmarshal(result, &s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "not configured") {
		t.Fatalf("expected not-configured message, got: %s", s)
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	t.Parallel()
	tool := NewWebSearch("some-key")
	args, _ := json.Marshal(webSearchArgs{})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestWebSearchWithMock(t *testing.T) {
	t.Parallel()
	tool := &WebSearchTool{
		provider: &mockSearchProvider{
			results: []SearchResult{
				{Title: "Go Programming", URL: "https://go.dev", Snippet: "An open-source programming language"},
				{Title: "Go Blog", URL: "https://go.dev/blog", Snippet: "The Go Blog"},
			},
		},
	}
	args, _ := json.Marshal(webSearchArgs{Query: "golang", MaxResults: 5})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var s string
	if err := json.Unmarshal(result, &s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "Go Programming") {
		t.Error("missing first result title")
	}
	if !strings.Contains(s, "go.dev/blog") {
		t.Error("missing second result URL")
	}
	if !strings.Contains(s, "1.") || !strings.Contains(s, "2.") {
		t.Error("results should be numbered")
	}
}

func TestWebSearchMaxResultsClamped(t *testing.T) {
	t.Parallel()
	var capturedMax int
	tool := &WebSearchTool{
		provider: &mockSearchProvider{},
	}
	// Override provider to capture max_results.
	tool.provider = searchCaptor{fn: func(_ context.Context, _ string, max int) ([]SearchResult, error) {
		capturedMax = max
		return nil, nil
	}}

	// Test default (0 → 10).
	args, _ := json.Marshal(webSearchArgs{Query: "test"})
	tool.Execute(context.Background(), args)
	if capturedMax != 10 {
		t.Errorf("default max_results = %d, want 10", capturedMax)
	}

	// Test clamp (30 → 20).
	args, _ = json.Marshal(webSearchArgs{Query: "test", MaxResults: 30})
	tool.Execute(context.Background(), args)
	if capturedMax != 20 {
		t.Errorf("clamped max_results = %d, want 20", capturedMax)
	}
}

type searchCaptor struct {
	fn func(ctx context.Context, query string, max int) ([]SearchResult, error)
}

func (c searchCaptor) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	return c.fn(ctx, query, max)
}

func TestWebSearchSchema(t *testing.T) {
	t.Parallel()
	tool := NewWebSearch("")
	s := tool.Schema()
	if s["type"] != "object" {
		t.Error("schema type should be object")
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("missing query property")
	}
}
