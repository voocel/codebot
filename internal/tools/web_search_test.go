package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestTavilySearchReal(t *testing.T) {
	key := os.Getenv("TAVILY_API_KEY")
	if key == "" {
		t.Skip("TAVILY_API_KEY not set")
	}

	tool := NewWebSearch("tavily", key)
	args, _ := json.Marshal(webSearchArgs{Query: "Go programming language", MaxResults: 3})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	var results []SearchResult
	json.Unmarshal(result, &results)
	for _, r := range results {
		t.Logf("title: %s\nurl: %s\nsnippet: %s\n", r.Title, r.URL, r.Snippet)
	}
}

func TestJinaSearchReal(t *testing.T) {
	key := os.Getenv("JINA_API_KEY")
	if key == "" {
		t.Skip("JINA_API_KEY not set")
	}

	tool := NewWebSearch("jina", key)
	args, _ := json.Marshal(webSearchArgs{Query: "Go programming language", MaxResults: 3})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	var results []SearchResult
	json.Unmarshal(result, &results)
	fmt.Printf("%+v\n", results)
}
