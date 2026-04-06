package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallLocalProjectPlugin(t *testing.T) {
	cwd := t.TempDir()
	src := filepath.Join(cwd, "src-plugin")
	writePlugin(t, src, map[string]any{
		"id":        "docs",
		"name":      "Docs",
		"version":   "0.1.0",
		"skillsDir": "./skills",
	})

	result, err := InstallLocal(InstallInput{
		Cwd:        cwd,
		SourcePath: src,
	})
	if err != nil {
		t.Fatalf("InstallLocal: %v", err)
	}
	if result.Scope != ScopeProject {
		t.Fatalf("scope = %q", result.Scope)
	}
	if _, err := os.Stat(filepath.Join(result.RootDir, "plugin.json")); err != nil {
		t.Fatalf("installed manifest missing: %v", err)
	}
}

func TestRemoveDeletesPluginDirAndState(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	root := filepath.Join(cwd, ".codebot", "plugins", "docs")
	writePlugin(t, root, map[string]any{
		"id":        "docs",
		"name":      "Docs",
		"version":   "0.1.0",
		"skillsDir": "./skills",
	})
	writeState(t, filepath.Join(cwd, ".codebot", "plugins-state.json"), map[string]any{
		"plugins": map[string]any{
			"docs": map[string]any{
				"enabled": false,
			},
		},
	})

	catalog, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	loaded, ok := findLoaded(catalog, "docs")
	if !ok {
		t.Fatal("expected docs plugin")
	}
	if err := Remove(cwd, loaded); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("expected plugin dir removed, got err=%v", err)
	}
	reloaded, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll after remove: %v", err)
	}
	if _, ok := findLoaded(reloaded, "docs"); ok {
		t.Fatal("expected docs plugin to be gone")
	}
}

func findLoaded(catalog *Catalog, id string) (Loaded, bool) {
	for _, loaded := range catalog.Plugins() {
		if loaded.Manifest.ID == id {
			return loaded, true
		}
	}
	return Loaded{}, false
}
