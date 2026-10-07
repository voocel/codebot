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

// snapshot is LiteLLM's model list as of the last go generate.
//
//go:embed models.json
var snapshot []byte

const (
	listCacheFile = "litellm-models.json"
	listCacheTTL  = 24 * time.Hour
	fetchTimeout  = 30 * time.Second
)

// Models serves the built-in snapshot until Refresh loads a current list.
type Models struct {
	catalog catalog.Catalog
}

func NewModels() *Models {
	m := &Models{}
	if err := m.catalog.LoadFromReader(bytes.NewReader(snapshot)); err != nil {
		panic(fmt.Sprintf("provider: built-in model list: %v", err))
	}
	return m
}

// Lookup uses provider.CatalogName for built-in types. Compat providers reach
// vendors litellm does not know, so it then tries the provider name as the
// vendor prefix (e.g. "moonshot/<model>"), then the bare model name.
func (m *Models) Lookup(spec ModelSpec) (catalog.Model, bool) {
	if name, ok := llmprovider.CatalogName(spec.Type, spec.Model); ok {
		return m.catalog.Get(name)
	}
	if facts, ok := m.catalog.Get(spec.Provider + "/" + spec.Model); ok {
		return facts, true
	}
	return m.catalog.Get(spec.Model)
}

// Refresh runs in the background and reuses the copy in cacheDir while it is
// under a day old.
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
