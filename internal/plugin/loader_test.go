package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAll_ProjectOverridesUserAndCollectsContributions(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	writePlugin(t, filepath.Join(home, ".codebot", "plugins", "assistant"), map[string]any{
		"id":          "assistant",
		"name":        "Assistant",
		"version":     "0.1.0",
		"skillsDir":   "./skills",
		"commandsDir": "./commands",
		"mcpServers": map[string]any{
			"context7": map[string]any{
				"command": "npx",
				"args":    []string{"-y", "@upstash/context7-mcp"},
			},
		},
	})
	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "assistant"), map[string]any{
		"id":          "assistant",
		"name":        "Assistant Project",
		"version":     "0.2.0",
		"skillsDir":   "./skills",
		"commandsDir": "./commands",
	})

	catalog, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	plugins := catalog.Plugins()
	if len(plugins) != 2 {
		t.Fatalf("expected builtin + project plugin, got %d", len(plugins))
	}
	foundProject := false
	for _, p := range plugins {
		if p.Manifest.ID == "assistant" {
			foundProject = true
			if p.Scope != "project" {
				t.Fatalf("expected project plugin to win, got %q", p.Scope)
			}
		}
	}
	if !foundProject {
		t.Fatal("expected assistant plugin to be present")
	}

	contrib := catalog.Contributions()
	if len(contrib.SkillSpecs) == 0 {
		t.Fatal("expected builtin plugin to contribute bundled skills")
	}
	if len(contrib.SkillDirs) != 1 {
		t.Fatalf("expected 1 external skill dir, got %d", len(contrib.SkillDirs))
	}
	if len(contrib.CommandDirs) != 1 {
		t.Fatalf("expected 1 command dir, got %d", len(contrib.CommandDirs))
	}
	if len(contrib.MCPServers) != 0 {
		t.Fatalf("expected project override to replace user plugin mcp, got %d entries", len(contrib.MCPServers))
	}
}

func TestLoadAll_DisabledPluginExcludedFromContributions(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "docs"), map[string]any{
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
	if got := len(catalog.Plugins()); got != 2 {
		t.Fatalf("expected builtin + project plugin to be discoverable, got %d", got)
	}
	if got := len(catalog.Contributions().SkillDirs); got != 0 {
		t.Fatalf("expected disabled project plugin to contribute no skill dirs, got %d", got)
	}
	if got := len(catalog.Contributions().SkillSpecs); got == 0 {
		t.Fatal("expected builtin plugin skills to remain enabled")
	}
}

func TestLoadAll_UntrustedPluginStripsPrivilegedContributions(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "ops"), map[string]any{
		"id":          "ops",
		"name":        "Ops",
		"version":     "0.1.0",
		"skillsDir":   "./skills",
		"commandsDir": "./commands",
		"mcpServers": map[string]any{
			"ops-mcp": map[string]any{
				"command": "npx",
				"args":    []string{"ops-mcp"},
			},
		},
	})
	writeState(t, filepath.Join(cwd, ".codebot", "plugins-state.json"), map[string]any{
		"plugins": map[string]any{
			"ops": map[string]any{
				"trust": "untrusted",
			},
		},
	})

	catalog, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	var ops Loaded
	found := false
	for _, loaded := range catalog.Plugins() {
		if loaded.Manifest.ID == "ops" {
			ops = loaded
			found = true
		}
	}
	if !found {
		t.Fatal("expected ops plugin to load")
	}
	if ops.IsTrusted() {
		t.Fatal("expected ops plugin to be untrusted")
	}

	contrib := catalog.Contributions()
	if _, ok := contrib.MCPServers["ops-mcp"]; ok {
		t.Fatal("expected untrusted plugin MCP server to be filtered")
	}
	if len(contrib.SkillDirs) == 0 || contrib.SkillDirs[0].Source != "remote" {
		t.Fatalf("expected untrusted plugin skills to load as remote, got %#v", contrib.SkillDirs)
	}
}

func TestLoadAll_RejectsDuplicateMCPServerNamesAcrossPlugins(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "docs"), map[string]any{
		"id":        "docs",
		"name":      "Docs",
		"version":   "0.1.0",
		"skillsDir": "./skills",
		"mcpServers": map[string]any{
			"shared": map[string]any{
				"command": "npx",
				"args":    []string{"docs-mcp"},
			},
		},
	})
	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "ops"), map[string]any{
		"id":        "ops",
		"name":      "Ops",
		"version":   "0.1.0",
		"skillsDir": "./skills",
		"mcpServers": map[string]any{
			"shared": map[string]any{
				"command": "npx",
				"args":    []string{"ops-mcp"},
			},
		},
	})

	_, err := LoadAll(cwd)
	if err == nil {
		t.Fatal("expected duplicate MCP server conflict")
	}
	if !strings.Contains(err.Error(), `duplicate MCP server "shared"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAll_AllowsDuplicateMCPServerNamesWhenOnePluginIsUntrusted(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "docs"), map[string]any{
		"id":        "docs",
		"name":      "Docs",
		"version":   "0.1.0",
		"skillsDir": "./skills",
		"mcpServers": map[string]any{
			"shared": map[string]any{
				"command": "npx",
				"args":    []string{"docs-mcp"},
			},
		},
	})
	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "ops"), map[string]any{
		"id":        "ops",
		"name":      "Ops",
		"version":   "0.1.0",
		"skillsDir": "./skills",
		"mcpServers": map[string]any{
			"shared": map[string]any{
				"command": "npx",
				"args":    []string{"ops-mcp"},
			},
		},
	})
	writeState(t, filepath.Join(cwd, ".codebot", "plugins-state.json"), map[string]any{
		"plugins": map[string]any{
			"ops": map[string]any{
				"trust": "untrusted",
			},
		},
	})

	catalog, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := catalog.Contributions().MCPServers["shared"]; !ok {
		t.Fatal("expected trusted plugin MCP server to remain available")
	}
}

func TestLoadAll_RejectsInvalidPluginID(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "bad"), map[string]any{
		"id":        "Bad Plugin",
		"name":      "Bad Plugin",
		"version":   "0.1.0",
		"skillsDir": "./skills",
	})

	_, err := LoadAll(cwd)
	if err == nil {
		t.Fatal("expected invalid plugin id to fail")
	}
	if !strings.Contains(err.Error(), "plugin id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAll_RejectsInvalidTrustInStateFile(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	writePlugin(t, filepath.Join(cwd, ".codebot", "plugins", "docs"), map[string]any{
		"id":        "docs",
		"name":      "Docs",
		"version":   "0.1.0",
		"skillsDir": "./skills",
	})
	writeState(t, filepath.Join(cwd, ".codebot", "plugins-state.json"), map[string]any{
		"plugins": map[string]any{
			"docs": map[string]any{
				"trust": "bogus",
			},
		},
	})

	_, err := LoadAll(cwd)
	if err == nil {
		t.Fatal("expected invalid trust in state file to fail")
	}
	if !strings.Contains(err.Error(), `invalid trust value "bogus"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAll_BuiltinStateRoundTrip(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	catalog, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	core, ok := findLoaded(catalog, "core")
	if !ok {
		t.Fatal("expected builtin core plugin")
	}

	if err := SetTrust(cwd, core, TrustUntrusted); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	if err := SetEnabled(cwd, core, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	reloaded, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll after round-trip: %v", err)
	}
	core, ok = findLoaded(reloaded, "core")
	if !ok {
		t.Fatal("expected builtin core plugin after reload")
	}
	if core.State.Enabled {
		t.Fatal("expected builtin core plugin to remain disabled after reload")
	}
	if core.State.Trust != TrustUntrusted {
		t.Fatalf("expected builtin core plugin trust=%q, got %q", TrustUntrusted, core.State.Trust)
	}
	if got := len(reloaded.Contributions().SkillSpecs); got != 0 {
		t.Fatalf("expected disabled builtin plugin to contribute no bundled skills, got %d", got)
	}
}

func TestLoadedContributionCountsReflectActualFiles(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	root := filepath.Join(cwd, ".codebot", "plugins", "ops")
	writePlugin(t, root, map[string]any{
		"id":          "ops",
		"name":        "Ops",
		"version":     "0.1.0",
		"skillsDir":   "./skills",
		"commandsDir": "./commands",
	})
	if err := os.WriteFile(filepath.Join(root, "skills", "triage.md"), []byte("---\ndescription: triage\n---\ntriage\n"), 0o644); err != nil {
		t.Fatalf("write skill triage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "review.md"), []byte("---\ndescription: review\n---\nreview\n"), 0o644); err != nil {
		t.Fatalf("write skill review: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "commands", "gate.md"), []byte("---\ndescription: gate\nusage: /gate\n---\nrun gate\n"), 0o644); err != nil {
		t.Fatalf("write command gate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "commands", "handoff.md"), []byte("---\ndescription: handoff\nusage: /handoff\n---\nrun handoff\n"), 0o644); err != nil {
		t.Fatalf("write command handoff: %v", err)
	}

	catalog, err := LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	ops, ok := findLoaded(catalog, "ops")
	if !ok {
		t.Fatal("expected ops plugin")
	}
	if got := ops.SkillCount(); got != 2 {
		t.Fatalf("SkillCount = %d, want 2", got)
	}
	if got := ops.CommandCount(); got != 2 {
		t.Fatalf("CommandCount = %d, want 2", got)
	}
}

func writePlugin(t *testing.T, root string, manifest map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir plugin root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func writeState(t *testing.T, path string, content map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
}
