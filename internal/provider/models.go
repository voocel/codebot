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
	llmprovider "github.com/voocel/litellm/provider"
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

// Lookup returns the facts for spec's model. Built-in provider types list it
// under the name provider.CatalogName gives; compat providers reach vendors
// litellm does not know, so the provider name is tried as the vendor prefix,
// as for one named "moonshot", before the bare model name.
func (m *Models) Lookup(spec ModelSpec) (catalog.Model, bool) {
	if name, ok := llmprovider.CatalogName(spec.Type, spec.Model); ok {
		return m.catalog.Get(name)
	}
	if facts, ok := m.catalog.Get(spec.Provider + "/" + spec.Model); ok {
		return facts, true
	}
	return m.catalog.Get(spec.Model)
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
