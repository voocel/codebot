package provider

//go:generate go run gen_models.go

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/voocel/litellm/catalog"
)

// snapshot is LiteLLM's model list as of the last go generate, trimmed to the
// vendors codebot users reach.
//
//go:embed models.json
var snapshot []byte

const (
	listCacheFile = "litellm-models.json"
	listCacheTTL  = 24 * time.Hour
	fetchTimeout  = 30 * time.Second
)

// catalogVendors maps the provider names whose LiteLLM prefix differs.
var catalogVendors = map[string]string{
	"glm":  "zai",
	"grok": "xai",
	"mimo": "xiaomi_mimo",
	"qwen": "dashscope",
}

// Models holds model facts (context window, output cap, reasoning, prices)
// from LiteLLM's model list: the built-in snapshot until Refresh loads a
// current one.
type Models struct {
	catalog catalog.Catalog
}

// NewModels returns the facts of the built-in snapshot.
func NewModels() *Models {
	m := &Models{}
	if err := m.catalog.LoadFromReader(bytes.NewReader(snapshot)); err != nil {
		panic(fmt.Sprintf("provider: built-in model list: %v", err))
	}
	return m
}

// Lookup returns the facts for spec's model, listed under the LiteLLM prefix
// of its provider type or, for custom providers such as a compat provider
// named "moonshot", of its provider name; OpenAI and Anthropic models, among
// others, are listed unprefixed.
func (m *Models) Lookup(spec ModelSpec) (catalog.Model, bool) {
	names := make([]string, 0, 3)
	for _, vendor := range []string{spec.Type, spec.Provider} {
		if v, ok := catalogVendors[vendor]; ok {
			vendor = v
		}
		names = append(names, vendor+"/"+spec.Model)
	}
	for _, name := range append(names, spec.Model) {
		if facts, ok := m.catalog.Get(name); ok {
			return facts, true
		}
	}
	return catalog.Model{}, false
}

// Refresh replaces the facts with LiteLLM's current list in the background,
// reusing the copy in cacheDir while it is under a day old.
func (m *Models) Refresh(cacheDir string) {
	go func() {
		if err := m.refresh(filepath.Join(cacheDir, listCacheFile)); err != nil {
			log.Printf("model list refresh: %v", err)
		}
	}()
}

func (m *Models) refresh(path string) error {
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < listCacheTTL {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return m.catalog.LoadFromReader(f)
	}
	data, err := fetchList()
	if err != nil {
		return err
	}
	if err := m.catalog.LoadFromReader(bytes.NewReader(data)); err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func fetchList() ([]byte, error) {
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(catalog.DefaultURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch model list: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".models-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
