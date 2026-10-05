package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

const fetchMaxRead = 5 << 20 // bytes downloaded at most

// webClient makes the web tools' requests.
var webClient = &http.Client{Timeout: 30 * time.Second}

// webNotConfigured is what the web tools answer without the API key their
// provider needs.
const webNotConfigured = "Web search and fetch are not configured: tavily needs an API key (search_api_key in the settings, or TAVILY_API_KEY), or set search_provider to jina."

type fetcher interface {
	fetch(ctx context.Context, targetURL string) (string, error)
}

// NewWebFetch returns the web_fetch tool, which fetches a web page as
// markdown through provider, "tavily" or "jina". Jina works without a key,
// tavily does not.
func NewWebFetch(provider, apiKey string) agentcore.Tool {
	var f fetcher // nil without the API key the provider needs
	switch {
	case provider == "jina":
		f = jinaFetcher{apiKey}
	case apiKey != "":
		f = tavilyFetcher{apiKey}
	}
	tool := agentcore.NewTool("web_fetch", "Fetch a web page and return markdown content.",
		schema.Object(
			schema.Property("url", schema.String("The URL to fetch (http/https)")).Required(),
			schema.Property("prompt", schema.String("Optional: what information to extract (included as context in output)")),
		),
		func(ctx context.Context, a webFetchArgs) (agentcore.Result, error) { return webFetch(ctx, f, a) },
	)
	tool.Label = "Fetch Web Page"
	return tool
}

type webFetchArgs struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

func webFetch(ctx context.Context, f fetcher, a webFetchArgs) (agentcore.Result, error) {
	if a.URL == "" {
		return agentcore.Result{}, errors.New("url is required")
	}
	targetURL, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil {
		return agentcore.Result{}, fmt.Errorf("invalid url: %w", err)
	}
	if scheme := strings.ToLower(targetURL.Scheme); scheme != "http" && scheme != "https" {
		return agentcore.Result{}, errors.New("only http/https URLs are supported")
	}
	if f == nil {
		return agentcore.TextResult(webNotConfigured), nil
	}

	content, err := f.fetch(ctx, targetURL.String())
	if err != nil {
		return agentcore.Result{}, fmt.Errorf("fetch failed: %w", err)
	}
	if a.Prompt != "" {
		content = fmt.Sprintf("> Extraction focus: %s\n\n%s", a.Prompt, content)
	}
	return agentcore.TextResult(content), nil
}

// tavilyFetcher extracts pages with POST https://api.tavily.com/extract.
type tavilyFetcher struct{ apiKey string }

type tavilyExtractResponse struct {
	Results []struct {
		RawContent string `json:"raw_content"`
	} `json:"results"`
	FailedResults []struct {
		Error string `json:"error"`
	} `json:"failed_results"`
}

func (p tavilyFetcher) fetch(ctx context.Context, targetURL string) (string, error) {
	body, err := json.Marshal(map[string]any{"urls": []string{targetURL}, "format": "markdown"})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/extract", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := webClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("tavily extract API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	var tr tavilyExtractResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, fetchMaxRead)).Decode(&tr); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(tr.Results) == 0 {
		if len(tr.FailedResults) > 0 {
			return "", fmt.Errorf("tavily extract failed: %s", tr.FailedResults[0].Error)
		}
		return "", errors.New("no content extracted")
	}
	return tr.Results[0].RawContent, nil
}

// jinaFetcher reads pages with GET https://r.jina.ai/{url}.
type jinaFetcher struct{ apiKey string }

func (p jinaFetcher) fetch(ctx context.Context, targetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://r.jina.ai/"+targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("X-Respond-With", "markdown")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := webClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch via jina reader: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("jina reader API error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxRead))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	return string(body), nil
}
