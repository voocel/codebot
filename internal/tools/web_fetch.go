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

	"codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/voocel/agentcore/schema"
)

const (
	fetchTimeout = 30 * time.Second
	fetchMaxRead = 5 * 1024 * 1024 // 5MB max download
	fetchMaxOut  = 50 * 1024       // 50KB output limit
	fetchUA      = "Mozilla/5.0 (compatible; Codebot/1.0)"
)

// WebFetchTool fetches a URL and converts its content to Markdown.
type WebFetchTool struct {
	Client *http.Client // nil uses default with timeout
}

func NewWebFetch() *WebFetchTool { return &WebFetchTool{} }

func (t *WebFetchTool) Name() string  { return "web_fetch" }
func (t *WebFetchTool) Label() string { return "Fetch Web Page" }
func (t *WebFetchTool) Description() string {
	return "Fetch a web page and convert its main content to Markdown. Strips ads, navigation, and scripts. Output is truncated to 50KB."
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
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(a.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme == "http" {
		parsed.Scheme = "https"
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http/https URLs are supported")
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fetchUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", parsed.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, parsed.Host)
	}

	body := io.LimitReader(resp.Body, fetchMaxRead)

	// Extract main content using readability.
	article, err := readability.FromReader(body, parsed)
	if err != nil {
		return nil, fmt.Errorf("extract content: %w", err)
	}
	if article.Node == nil {
		return json.Marshal(fmt.Sprintf("Could not extract readable content from %s", parsed.Host))
	}

	// Convert the readability node to Markdown.
	domain := parsed.Scheme + "://" + parsed.Host
	md, err := htmltomarkdown.ConvertNode(article.Node, converter.WithDomain(domain))
	if err != nil {
		// Fallback: render as plain text.
		var buf bytes.Buffer
		if rerr := article.RenderText(&buf); rerr != nil {
			return nil, fmt.Errorf("convert content: %w", err)
		}
		md = buf.Bytes()
	}

	var sb strings.Builder
	if title := article.Title(); title != "" {
		fmt.Fprintf(&sb, "# %s\n\n", title)
	}
	if a.Prompt != "" {
		fmt.Fprintf(&sb, "> Extraction focus: %s\n\n", a.Prompt)
	}
	sb.Write(md)

	result := sb.String()
	if len(result) > fetchMaxOut {
		result = result[:fetchMaxOut] + "\n\n[Content truncated at 50KB]"
	}

	return json.Marshal(result)
}
