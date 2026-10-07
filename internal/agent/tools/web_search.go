package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"content"`
}

type searcher interface {
	search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
}

func NewWebSearch(provider, apiKey string) agentcore.Tool {
	var s searcher // nil without the provider's API key
	switch {
	case provider == "jina":
		s = jinaSearcher{apiKey}
	case apiKey != "":
		s = tavilySearcher{apiKey}
	}
	tool := agentcore.NewTool("web_search",
		"Search the web for current information. Returns titles, URLs, and snippets for each result.",
		schema.Object(
			schema.Property("query", schema.String("The search query")).Required(),
			schema.Property("max_results", schema.Int("Maximum results to return (default: 10, max: 20)")),
		),
		func(ctx context.Context, a webSearchArgs) (agentcore.Result, error) { return webSearch(ctx, s, a) },
	)
	tool.Label = "Web Search"
	return tool
}

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func webSearch(ctx context.Context, s searcher, a webSearchArgs) (agentcore.Result, error) {
	if a.Query == "" {
		return agentcore.Result{}, errors.New("query is required")
	}
	if s == nil {
		return agentcore.TextResult(webNotConfigured), nil
	}

	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	results, err := s.search(ctx, a.Query, min(maxResults, 20))
	if err != nil {
		return agentcore.Result{}, fmt.Errorf("search failed: %w", err)
	}
	return agentcore.JSONResult(results)
}

type tavilySearcher struct{ apiKey string }

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func (p tavilySearcher) search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	body, err := json.Marshal(map[string]any{"query": query, "max_results": maxResults})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := webClient.Do(req)
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

// jinaSearcher sets X-Respond-With: no-content so Jina returns only titles,
// URLs and snippets.
type jinaSearcher struct{ apiKey string }

type jinaSearchResponse struct {
	Data []struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Content     string `json:"content"`
	} `json:"data"`
}

func (p jinaSearcher) search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	body, err := json.Marshal(map[string]string{"q": query})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://s.jina.ai/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Respond-With", "no-content")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := webClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("jina API error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	var jr jinaSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&jr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	rows := jr.Data[:min(len(jr.Data), maxResults)]
	results := make([]SearchResult, len(rows))
	for i, r := range rows {
		snippet := strings.TrimSpace(r.Description)
		if snippet == "" {
			snippet = strings.TrimSpace(r.Content)
		}
		results[i] = SearchResult{Title: strings.TrimSpace(r.Title), URL: strings.TrimSpace(r.URL), Snippet: snippet}
	}
	return results, nil
}
