package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestTavilySearchReal(t *testing.T) {
	key := "TAVILY_API_KEY"

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
	key := "JINA_API_KEY"

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
