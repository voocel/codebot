//go:build ignore

// gen_models.go snapshots LiteLLM's model list into models.json, keeping the
// chat models of the vendors codebot users reach and the fields
// catalog.LoadFromReader reads.
// Usage: go generate ./internal/provider/...

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"time"

	"github.com/voocel/litellm/catalog"
)

// vendors are LiteLLM provider names: those codebot builds, plus those
// reached through a compat provider.
var vendors = []string{
	"anthropic", "bedrock", "bedrock_converse", "dashscope", "deepseek",
	"gemini", "minimax", "mistral", "moonshot", "ollama", "openai",
	"openrouter", "xai", "xiaomi_mimo", "zai",
}

// read reports whether catalog.LoadFromReader reads a field: the model's
// facts, its rates, those of its long-input tiers, as
// input_cost_per_token_above_200k_tokens, and a tiered_pricing table.
func read(field string) bool {
	return slices.Contains(fields, field) || field == "tiered_pricing" || tierRate.MatchString(field)
}

var fields = []string{
	"mode", "litellm_provider", "max_input_tokens", "max_output_tokens",
	"supports_reasoning", "input_cost_per_token", "output_cost_per_token",
	"cache_read_input_token_cost", "cache_creation_input_token_cost",
	"cache_creation_input_token_cost_above_1hr",
}

var tierRate = regexp.MustCompile(`^(input_cost_per_token|output_cost_per_token|cache_read_input_token_cost|cache_creation_input_token_cost|cache_creation_input_token_cost_above_1hr)_above_\d+k_tokens$`)

func main() {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(catalog.DefaultURL)
	if err != nil {
		log.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("fetch: HTTP %d", resp.StatusCode)
	}
	var list map[string]map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		log.Fatalf("decode: %v", err)
	}

	kept := make(map[string]map[string]any)
	for name, entry := range list {
		mode, _ := entry["mode"].(string)
		vendor, _ := entry["litellm_provider"].(string)
		if (mode != "chat" && mode != "responses") || !slices.Contains(vendors, vendor) {
			continue
		}
		trimmed := make(map[string]any, len(fields))
		for field, value := range entry {
			if read(field) {
				trimmed[field] = value
			}
		}
		kept[name] = trimmed
	}

	// One model per line keeps regenerated diffs readable.
	var buf bytes.Buffer
	buf.WriteString("{\n")
	names := slices.Sorted(maps.Keys(kept))
	for i, name := range names {
		key, _ := json.Marshal(name)
		value, err := json.Marshal(kept[name])
		if err != nil {
			log.Fatalf("encode %s: %v", name, err)
		}
		sep := ","
		if i == len(names)-1 {
			sep = ""
		}
		fmt.Fprintf(&buf, "  %s: %s%s\n", key, value, sep)
	}
	buf.WriteString("}\n")

	var c catalog.Catalog
	if err := c.LoadFromReader(bytes.NewReader(buf.Bytes())); err != nil {
		log.Fatalf("validate: %v", err)
	}
	if err := os.WriteFile("models.json", buf.Bytes(), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Printf("snapshotted %d models\n", len(names))
}
