package tools

import (
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
