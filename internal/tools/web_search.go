package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
)

// SearchResult represents a single web search result.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"content"`
}

// SearchProvider performs web searches.
type SearchProvider interface {
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
}

// WebSearchTool provides web search capability.
type WebSearchTool struct {
	provider SearchProvider
}

// NewWebSearch creates a WebSearchTool. If apiKey is non-empty, a TavilyProvider is used.
func NewWebSearch(apiKey string) *WebSearchTool {
	var provider SearchProvider
	if apiKey != "" {
		provider = &TavilyProvider{APIKey: apiKey}
	}
	return &WebSearchTool{provider: provider}
}

func (t *WebSearchTool) Name() string        { return "web_search" }
func (t *WebSearchTool) Label() string        { return "Web Search" }
func (t *WebSearchTool) Description() string {
	return "Search the web for current information. Returns titles, URLs, and snippets for each result."
}
func (t *WebSearchTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("query", schema.String("The search query")).Required(),
		schema.Property("max_results", schema.Int("Maximum results to return (default: 10, max: 20)")),
	)
}

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (t *WebSearchTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a webSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if t.provider == nil {
		return json.Marshal("Web search is not configured. Set TAVILY_API_KEY environment variable or configure search_api_key in settings to enable.")
	}

	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 20 {
		maxResults = 20
	}

	results, err := t.provider.Search(ctx, a.Query, maxResults)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	if len(results) == 0 {
		return json.Marshal("No results found.")
	}

	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. [%s](%s)\n   %s\n\n", i+1, r.Title, r.URL, r.Snippet)
	}
	return json.Marshal(strings.TrimSpace(sb.String()))
}

// ---------------------------------------------------------------------------
// Tavily search provider
// ---------------------------------------------------------------------------

// TavilyProvider implements SearchProvider using the Tavily API.
type TavilyProvider struct {
	APIKey string
	Client *http.Client // nil uses default with 30s timeout
}

type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type tavilyResponse struct {
	Results []struct {
		Title   string  `json:"title"`
		URL     string  `json:"url"`
		Content string  `json:"content"`
		Score   float64 `json:"score"`
	} `json:"results"`
}

func (p *TavilyProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	body, err := json.Marshal(tavilyRequest{
		Query:      query,
		MaxResults: maxResults,
	})
	if err != nil {
		return nil, err
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tavily API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	var tr tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	results := make([]SearchResult, len(tr.Results))
	for i, r := range tr.Results {
		results[i] = SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content}
	}
	return results, nil
}
