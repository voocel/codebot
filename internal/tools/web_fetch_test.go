package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTavilyFetchReal(t *testing.T) {
	key := "TAVILY_API_KEY"

	tool := NewWebFetch("tavily", key)
	args, _ := json.Marshal(webFetchArgs{URL: "https://go.dev"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}

	var md string
	json.Unmarshal(result, &md)
	t.Log(md)
}

func TestJinaFetchReal(t *testing.T) {
	key := "JINA_API_KEY"

	tool := NewWebFetch("jina", key)
	args, _ := json.Marshal(webFetchArgs{URL: "https://go.dev"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}

	var md string
	json.Unmarshal(result, &md)
	t.Log(md)
}
