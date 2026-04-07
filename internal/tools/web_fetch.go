package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/apperr"
)

const (
	fetchTimeout = 30 * time.Second
	fetchMaxRead = 5 * 1024 * 1024 // 5MB max download
	fetchMaxOut  = 50 * 1024       // 50KB output limit
	jinaReadURL  = "https://r.jina.ai/"
)

// FetchProvider performs web page content extraction.
type FetchProvider interface {
	Fetch(ctx context.Context, targetURL string) (string, error)
}

// WebFetchTool fetches web content and returns markdown.
type WebFetchTool struct {
	provider     FetchProvider
	providerName string
}

// NewWebFetch creates a WebFetchTool for the configured provider.
// Supported providers: tavily, jina.
func NewWebFetch(providerName, apiKey string) *WebFetchTool {
	name := strings.ToLower(strings.TrimSpace(providerName))
	switch name {
	case "tavily":
		var provider FetchProvider
		if apiKey != "" {
			provider = &TavilyFetchProvider{APIKey: apiKey}
		}
		return &WebFetchTool{provider: provider, providerName: "tavily"}
	default:
		return &WebFetchTool{
			provider:     &JinaFetchProvider{APIKey: apiKey},
			providerName: "jina",
		}
	}
}

func (t *WebFetchTool) Name() string  { return "web_fetch" }
func (t *WebFetchTool) Label() string { return "Fetch Web Page" }
func (t *WebFetchTool) Description() string {
	return "Fetch a web page and return markdown content. Output is truncated to 50KB."
}
func (t *WebFetchTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("url", schema.String("The URL to fetch (http/https)")).Required(),
		schema.Property("prompt", schema.String("Optional: what information to extract (included as context in output)")),
	)
}

type webFetchArgs struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

func (t *WebFetchTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a webFetchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, apperr.WrapKind(apperr.KindToolInput, "invalid args", err)
	}
	if a.URL == "" {
		return nil, apperr.NewKind(apperr.KindToolInput, "url is required")
	}

	targetURL, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindToolInput, "invalid url", err)
	}
	switch strings.ToLower(targetURL.Scheme) {
	case "http", "https":
	default:
		return nil, apperr.NewKind(apperr.KindToolInput, "only http/https URLs are supported")
	}

	if t.provider == nil {
		if t.providerName != "" {
			return json.Marshal(fmt.Sprintf("Web fetch provider %q is not configured. Supported providers: tavily, jina.", t.providerName))
		}
		return json.Marshal("Web fetch is not configured. Set search_provider to tavily/jina and configure search_api_key, TAVILY_API_KEY, or JINA_API_KEY as appropriate.")
	}

	content, err := t.provider.Fetch(ctx, targetURL.String())
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindToolExec, "fetch failed", err)
	}

	var sb strings.Builder
	if a.Prompt != "" {
		fmt.Fprintf(&sb, "> Extraction focus: %s\n\n", a.Prompt)
	}
	sb.WriteString(content)

	result := sb.String()
	if len(result) > fetchMaxOut {
		result = result[:fetchMaxOut] + "\n\n[Content truncated at 50KB]"
	}

	return json.Marshal(result)
}

// ---------------------------------------------------------------------------
// Tavily fetch provider (POST https://api.tavily.com/extract)
// ---------------------------------------------------------------------------

type TavilyFetchProvider struct {
	APIKey string
	Client *http.Client
}

type tavilyExtractRequest struct {
	URLs   []string `json:"urls"`
	Format string   `json:"format,omitempty"`
}

type tavilyExtractResponse struct {
	Results []struct {
		URL        string `json:"url"`
		RawContent string `json:"raw_content"`
	} `json:"results"`
	FailedResults []struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	} `json:"failed_results"`
}

func (p *TavilyFetchProvider) Fetch(ctx context.Context, targetURL string) (string, error) {
	body, err := json.Marshal(tavilyExtractRequest{
		URLs:   []string{targetURL},
		Format: "markdown",
	})
	if err != nil {
		return "", err
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/extract", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("tavily extract API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxRead))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var tr tavilyExtractResponse
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(tr.Results) == 0 {
		if len(tr.FailedResults) > 0 {
			return "", fmt.Errorf("tavily extract failed: %s", tr.FailedResults[0].Error)
		}
		return "", fmt.Errorf("no content extracted")
	}
	return tr.Results[0].RawContent, nil
}

// ---------------------------------------------------------------------------
// Jina fetch provider (GET https://r.jina.ai/{url})
// ---------------------------------------------------------------------------

type JinaFetchProvider struct {
	APIKey  string
	Client  *http.Client
	BaseURL string
}

func (p *JinaFetchProvider) Fetch(ctx context.Context, targetURL string) (string, error) {
	baseURL := strings.TrimSpace(p.BaseURL)
	if baseURL == "" {
		baseURL = jinaReadURL
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("X-Respond-With", "markdown")
	if strings.TrimSpace(p.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.APIKey))
	}

	resp, err := client.Do(req)
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
