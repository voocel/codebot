package tools

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/voocel/agentcore"
)

func TestTavilyWithoutKeyIsNotConfigured(t *testing.T) {
	t.Parallel()

	for _, tool := range []agentcore.Tool{NewWebFetch("tavily", ""), NewWebSearch("tavily", "")} {
		text, err := call(t, tool, `{"url":"https://go.dev","query":"go"}`)
		if err != nil {
			t.Fatal(err)
		}
		if text != webNotConfigured {
			t.Fatalf("%s: got %s", tool.Name, text)
		}
	}
}

func TestTavilyFetchReal(t *testing.T) {
	key := os.Getenv("TAVILY_API_KEY")
	if key == "" {
		t.Skip("TAVILY_API_KEY not set")
	}
	fetchReal(t, NewWebFetch("tavily", key))
}

func TestJinaFetchReal(t *testing.T) {
	key := os.Getenv("JINA_API_KEY")
	if key == "" {
		t.Skip("JINA_API_KEY not set")
	}
	fetchReal(t, NewWebFetch("jina", key))
}

func fetchReal(t *testing.T, tool agentcore.Tool) {
	args, _ := json.Marshal(webFetchArgs{URL: "https://go.dev"})
	md, err := call(t, tool, string(args))
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}
	t.Log(md)
}
