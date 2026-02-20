package provider

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/config"
)

const (
	openRouterURL = "https://openrouter.ai/api/v1/models"
	cacheTTL      = 24 * time.Hour
	fetchTimeout  = 10 * time.Second
	cacheFileName = "models-cache.json"
)

var providerMap = map[string]string{
	"anthropic": "anthropic",
	"openai":    "openai",
	"google":    "gemini",
}

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

type openRouterModel struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	ContextLength int                   `json:"context_length"`
	Pricing       *openRouterPricing    `json:"pricing"`
	TopProvider   *openRouterTopProvider `json:"top_provider"`
}

type openRouterPricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read"`
	InputCacheWrite string `json:"input_cache_write"`
}

type openRouterTopProvider struct {
	MaxCompletionTokens int `json:"max_completion_tokens"`
}

type modelCache struct {
	FetchedAt time.Time    `json:"fetched_at"`
	Models    []ModelEntry `json:"models"`
}

// StartPricingRefresh launches a background goroutine that refreshes model data
// from the OpenRouter API. Disk cache (~/.codebot/models-cache.json) is checked
// first with a 24h TTL. Fresh data is merged into the registry, overlaying the
// compiled-in baseline from models_generated.go.
func StartPricingRefresh(registry *ModelRegistry) {
	go func() {
		models := loadCache()
		if models == nil {
			fetched, err := fetchModels()
			if err != nil {
				log.Printf("pricing refresh: %v", err)
				return
			}
			models = fetched
			saveCache(models)
		}
		registry.MergeModels(models)
	}()
}

func cachePath() string {
	dir := config.UserConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, cacheFileName)
}

func loadCache() []ModelEntry {
	p := cachePath()
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var c modelCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if time.Since(c.FetchedAt) > cacheTTL {
		return nil
	}
	return c.Models
}

func saveCache(models []ModelEntry) {
	p := cachePath()
	if p == "" {
		return
	}
	dir := filepath.Dir(p)
	_ = os.MkdirAll(dir, 0o755)
	data, err := json.Marshal(modelCache{FetchedAt: time.Now(), Models: models})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".models-cache-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return
	}
	tmp.Close()
	if err := os.Rename(tmpPath, p); err != nil {
		os.Remove(tmpPath)
	}
}

func fetchModels() ([]ModelEntry, error) {
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(openRouterURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter API returned %d", resp.StatusCode)
	}

	var result openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []ModelEntry
	for _, m := range result.Data {
		if e, ok := convertModel(m); ok {
			models = append(models, e)
		}
	}
	return models, nil
}

func convertModel(m openRouterModel) (ModelEntry, bool) {
	parts := strings.SplitN(m.ID, "/", 2)
	if len(parts) != 2 {
		return ModelEntry{}, false
	}
	prov, ok := providerMap[parts[0]]
	if !ok {
		return ModelEntry{}, false
	}
	modelID := parts[1]
	if strings.Contains(modelID, ":") {
		return ModelEntry{}, false
	}

	entry := ModelEntry{
		Provider:      prov,
		ID:            modelID,
		Name:          cleanModelName(m.Name),
		ContextWindow: m.ContextLength,
		Reasoning:     inferReasoning(prov, modelID),
	}
	if m.TopProvider != nil {
		entry.MaxTokens = m.TopProvider.MaxCompletionTokens
	}
	if m.Pricing != nil {
		entry.InputCostPer1M = tokenToMillion(m.Pricing.Prompt)
		entry.OutputCostPer1M = tokenToMillion(m.Pricing.Completion)
		entry.CacheReadCostPer1M = tokenToMillion(m.Pricing.InputCacheRead)
		entry.CacheWriteCostPer1M = tokenToMillion(m.Pricing.InputCacheWrite)
	}
	return entry, true
}

func tokenToMillion(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return math.Round(v*1e6*1e6) / 1e6
}

func cleanModelName(name string) string {
	if _, after, ok := strings.Cut(name, ": "); ok {
		return after
	}
	return name
}

// inferReasoning guesses whether a model supports extended thinking based on its ID.
func inferReasoning(provider, modelID string) bool {
	id := strings.ToLower(modelID)
	switch provider {
	case "anthropic":
		// Claude 3.7+ sonnet, 4+ opus/sonnet/haiku support thinking.
		return strings.Contains(id, "sonnet-4") || strings.Contains(id, "opus-4") ||
			strings.Contains(id, "haiku-4") || strings.Contains(id, "3.7-sonnet")
	case "openai":
		// o-series models (o1, o3, o4, etc.)
		return len(id) >= 2 && id[0] == 'o' && id[1] >= '0' && id[1] <= '9'
	case "gemini":
		return strings.HasPrefix(id, "gemini-2.5-") || strings.HasPrefix(id, "gemini-3")
	}
	return false
}
