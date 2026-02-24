package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchEmptyURL(t *testing.T) {
	t.Parallel()
	tool := NewWebFetch()
	args, _ := json.Marshal(webFetchArgs{})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for empty url")
	}
}

func TestWebFetchInvalidScheme(t *testing.T) {
	t.Parallel()
	tool := NewWebFetch()
	args, _ := json.Marshal(webFetchArgs{URL: "ftp://example.com"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "only http/https") {
		t.Fatalf("expected scheme error, got: %v", err)
	}
}

func TestWebFetchHTMLToMarkdown(t *testing.T) {
	t.Parallel()

	const page = `<!DOCTYPE html>
<html><head><title>Test Page</title></head>
<body>
<nav>Navigation</nav>
<article>
<h1>Hello World</h1>
<p>This is a <strong>test</strong> paragraph with a <a href="/link">link</a>.</p>
<ul><li>Item 1</li><li>Item 2</li></ul>
</article>
<footer>Footer</footer>
</body></html>`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	tool := &WebFetchTool{
		Client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	args, _ := json.Marshal(webFetchArgs{URL: srv.URL, Prompt: "extract main content"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var md string
	if err := json.Unmarshal(result, &md); err != nil {
		t.Fatal(err)
	}

	// Should contain the article content.
	if !strings.Contains(md, "Hello World") {
		t.Error("missing article heading")
	}
	if !strings.Contains(md, "test") {
		t.Error("missing paragraph text")
	}
	// Should contain the extraction prompt context.
	if !strings.Contains(md, "Extraction focus") {
		t.Error("missing prompt context")
	}
}

func TestWebFetchTruncation(t *testing.T) {
	t.Parallel()

	// Generate a page with content exceeding 50KB.
	var sb strings.Builder
	sb.WriteString(`<html><head><title>Big</title></head><body><article>`)
	for range 10000 {
		sb.WriteString("<p>This is a long paragraph of filler text to exceed output limit.</p>\n")
	}
	sb.WriteString(`</article></body></html>`)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	tool := &WebFetchTool{
		Client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	args, _ := json.Marshal(webFetchArgs{URL: srv.URL})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var md string
	json.Unmarshal(result, &md)
	if !strings.Contains(md, "[Content truncated at 50KB]") {
		t.Error("expected truncation notice")
	}
}

func TestWebFetchEmptyContent(t *testing.T) {
	t.Parallel()

	// A page with no article-like content.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head></head><body></body></html>`))
	}))
	defer srv.Close()

	tool := &WebFetchTool{
		Client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	args, _ := json.Marshal(webFetchArgs{URL: srv.URL})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var s string
	json.Unmarshal(result, &s)
	if !strings.Contains(s, "Could not extract") {
		t.Errorf("expected fallback message, got: %s", s)
	}
}

func TestWebFetchSchema(t *testing.T) {
	t.Parallel()
	tool := NewWebFetch()
	s := tool.Schema()
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing properties")
	}
	if _, ok := props["url"]; !ok {
		t.Error("missing url property")
	}
	if _, ok := props["prompt"]; !ok {
		t.Error("missing prompt property")
	}
}
