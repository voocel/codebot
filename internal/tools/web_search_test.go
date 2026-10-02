package tools

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTavilySearchReal(t *testing.T) {
	searchReal(t, "tavily", "TAVILY_API_KEY")
}

func TestJinaSearchReal(t *testing.T) {
	searchReal(t, "jina", "JINA_API_KEY")
}

func searchReal(t *testing.T, provider, keyVar string) {
	key := os.Getenv(keyVar)
	if key == "" {
		t.Skip(keyVar + " not set")
	}
	args, _ := json.Marshal(webSearchArgs{Query: "Go programming language", MaxResults: 3})
	out, err := call(t, NewWebSearch(provider, key), string(args))
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	var results []SearchResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	for _, r := range results {
		t.Logf("title: %s\nurl: %s\nsnippet: %s\n", r.Title, r.URL, r.Snippet)
	}
}
