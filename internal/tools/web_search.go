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
	provider     SearchProvider
	providerName string
}

// NewWebSearch creates a WebSearchTool for the configured provider.
// Supported providers: tavily, jina.
func NewWebSearch(providerName, apiKey string) *WebSearchTool {
	name := strings.ToLower(strings.TrimSpace(providerName))
	switch name {
	case "", "tavily":
		var provider SearchProvider
		if apiKey != "" {
			provider = &TavilyProvider{APIKey: apiKey}
		}
		return &WebSearchTool{
			provider:     provider,
			providerName: "tavily",
		}
	case "jina", "jina.ai", "jinaai":
		return &WebSearchTool{
			provider:     &JinaProvider{APIKey: apiKey},
			providerName: "jina",
		}
	default:
		return &WebSearchTool{providerName: name}
	}
}

func (t *WebSearchTool) Name() string  { return "web_search" }
func (t *WebSearchTool) Label() string { return "Web Search" }
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
		if t.providerName != "" {
			return json.Marshal(fmt.Sprintf("Web search provider %q is not configured. Supported providers: tavily, jina.", t.providerName))
		}
		return json.Marshal("Web search is not configured. Set search_provider to tavily/jina. For tavily, configure TAVILY_API_KEY or search_api_key.")
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
		return json.Marshal([]SearchResult{})
	}

	return json.Marshal(results)
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

// ---------------------------------------------------------------------------
// Jina search provider
// ---------------------------------------------------------------------------

// JinaProvider implements SearchProvider using the Jina search endpoint.
// It calls POST https://s.jina.ai/ with X-Respond-With: no-content
// to return only SERP metadata (title, URL, snippet) without full page content.
type JinaProvider struct {
	APIKey  string
	Client  *http.Client // nil uses default with 30s timeout
	BaseURL string       // default: https://s.jina.ai/
}

type jinaSearchRequest struct {
	Query string `json:"q"`
}

type jinaSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

type jinaSearchResponse struct {
	Data []jinaSearchResult `json:"data"`
}

func (p *JinaProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	baseURL := strings.TrimSpace(p.BaseURL)
	if baseURL == "" {
		baseURL = "https://s.jina.ai/"
	}

	body, err := json.Marshal(jinaSearchRequest{Query: query})
	if err != nil {
		return nil, err
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Respond-With", "no-content")
	if strings.TrimSpace(p.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.APIKey))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		snippet := string(respBody)
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return nil, fmt.Errorf("jina API error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(snippet))
	}

	var jr jinaSearchResponse
	if err := json.Unmarshal(respBody, &jr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	rows := jr.Data
	if len(rows) == 0 {
		return nil, nil
	}
	if maxResults > 0 && len(rows) > maxResults {
		rows = rows[:maxResults]
	}

	results := make([]SearchResult, 0, len(rows))
	for _, r := range rows {
		snippet := strings.TrimSpace(r.Description)
		if snippet == "" {
			snippet = strings.TrimSpace(r.Content)
		}
		results = append(results, SearchResult{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Snippet: snippet,
		})
	}
	return results, nil
}
