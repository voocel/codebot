package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
)

type rawState struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Trust   string `json:"trust,omitempty"`
}

type stateFile struct {
	Plugins map[string]rawState `json:"plugins"`
}

// LoadAll discovers plugins from user and project roots. Project scope wins on duplicate IDs.
func LoadAll(cwd string) (*Catalog, error) {
	userStates, err := loadStateFile(userStatePath())
	if err != nil {
		return nil, err
	}
	projectStates, err := loadStateFile(projectStatePath(cwd))
	if err != nil {
		return nil, err
	}

	byID := make(map[string]Loaded)
	builtin, err := loadBuiltinPlugins(cwd, userStates, projectStates)
	if err != nil {
		return nil, err
	}
	for _, p := range builtin {
		byID[p.Manifest.ID] = p
	}
	for _, scope := range []struct {
		name   string
		root   string
		states map[string]rawState
	}{
		{name: "user", root: userPluginsDir(), states: userStates},
		{name: "project", root: projectPluginsDir(cwd), states: projectStates},
	} {
		plugins, err := loadScope(scope.root, scope.name, scope.states)
		if err != nil {
			return nil, err
		}
		for _, p := range plugins {
			byID[p.Manifest.ID] = p
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	plugins := make([]Loaded, 0, len(ids))
	for _, id := range ids {
		plugins = append(plugins, byID[id])
	}
	if err := validateCatalog(plugins); err != nil {
		return nil, err
	}
	return &Catalog{plugins: plugins}, nil
}

func loadScope(root, scope string, states map[string]rawState) ([]Loaded, error) {
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s plugins dir: %w", scope, err)
	}

	var plugins []Loaded
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rootDir := filepath.Join(root, entry.Name())
		manifestPath := filepath.Join(rootDir, "plugin.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read plugin manifest %s: %w", manifestPath, err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("parse plugin manifest %s: %w", manifestPath, err)
		}
		if err := validateManifest(manifestPath, rootDir, manifest); err != nil {
			return nil, err
		}
		state, err := applyState(scope, manifest.ID, states[manifest.ID])
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, Loaded{
			Manifest: manifest,
			State:    state,
			RootDir:  rootDir,
			Scope:    scope,
		})
	}
	return plugins, nil
}

func applyState(scope, id string, raw rawState) (State, error) {
	state := State{
		Enabled: true,
		Trust:   DefaultTrust(scope),
	}
	if raw.Enabled != nil {
		state.Enabled = *raw.Enabled
	}
	if strings.TrimSpace(raw.Trust) != "" {
		trust := NormalizeTrust(raw.Trust)
		if trust == "" {
			return State{}, fmt.Errorf("plugin %s (%s): invalid trust value %q in state file", id, scope, raw.Trust)
		}
		state.Trust = trust
	}
	return state, nil
}

func loadBuiltinPlugins(cwd string, userStates, projectStates map[string]rawState) ([]Loaded, error) {
	specs := skill.BundledSpecs(cwd)
	if len(specs) == 0 {
		return nil, nil
	}
	raw := userStates["core"]
	if override, ok := projectStates["core"]; ok {
		raw = override
	}
	state, err := applyState("builtin", "core", raw)
	if err != nil {
		return nil, err
	}
	return []Loaded{{
		Manifest: Manifest{
			ID:          "core",
			Name:        "Core",
			Version:     "builtin",
			Description: "Built-in codebot skills",
		},
		State:      state,
		Scope:      "builtin",
		skillSpecs: specs,
	}}, nil
}

func validateManifest(path, root string, manifest Manifest) error {
	if strings.TrimSpace(manifest.ID) == "" {
		return fmt.Errorf("plugin manifest %s: id is required", path)
	}
	if err := ValidateID(manifest.ID); err != nil {
		return fmt.Errorf("plugin manifest %s: %w", path, err)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return fmt.Errorf("plugin manifest %s: name is required", path)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return fmt.Errorf("plugin manifest %s: version is required", path)
	}
	for _, rel := range []string{manifest.SkillsDir, manifest.CommandsDir} {
		if rel == "" {
			continue
		}
		if _, err := resolveRelativeDir(root, rel); err != nil {
			return fmt.Errorf("plugin manifest %s: %w", path, err)
		}
	}
	return nil
}

func resolveRelativeDir(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute contribution path %q is not allowed", rel)
	}
	cleanRoot := filepath.Clean(root)
	joined := filepath.Clean(filepath.Join(cleanRoot, rel))
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes plugin root", rel)
	}
	info, err := os.Stat(joined)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", joined, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", joined)
	}
	return joined, nil
}

func loadStateFile(path string) (map[string]rawState, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plugin state %s: %w", path, err)
	}
	var raw stateFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse plugin state %s: %w", path, err)
	}
	return raw.Plugins, nil
}

func validateCatalog(plugins []Loaded) error {
	type contributor struct {
		id    string
		scope string
	}

	seen := make(map[string]contributor)
	for _, loaded := range plugins {
		if !loaded.State.Enabled {
			continue
		}
		for name := range AllowedMCPServers(loaded.State.Trust, loaded.Manifest.MCPServers) {
			if prev, exists := seen[name]; exists {
				return fmt.Errorf("duplicate MCP server %q contributed by plugins %s (%s) and %s (%s)", name, prev.id, prev.scope, loaded.Manifest.ID, loaded.Scope)
			}
			seen[name] = contributor{id: loaded.Manifest.ID, scope: loaded.Scope}
		}
	}
	return nil
}

// SetEnabled persists a plugin enable/disable decision into the matching state file.
func SetEnabled(cwd string, loaded Loaded, enabled bool) error {
	path := statePathForScope(cwd, loaded.Scope)
	if path == "" {
		return fmt.Errorf("no writable state path for plugin scope %q", loaded.Scope)
	}
	current, err := loadStateFile(path)
	if err != nil {
		return err
	}
	if current == nil {
		current = make(map[string]rawState)
	}
	entry := current[loaded.Manifest.ID]
	entry.Enabled = boolPtr(enabled)
	if NormalizeTrust(entry.Trust) == "" {
		entry.Trust = loaded.State.Trust
	}
	current[loaded.Manifest.ID] = entry
	return writeStateFile(path, current)
}

// SetTrust persists a plugin trust decision into the matching state file.
func SetTrust(cwd string, loaded Loaded, trust string) error {
	trust = NormalizeTrust(trust)
	if trust == "" {
		return fmt.Errorf("invalid trust value")
	}
	path := statePathForScope(cwd, loaded.Scope)
	if path == "" {
		return fmt.Errorf("no writable state path for plugin scope %q", loaded.Scope)
	}
	current, err := loadStateFile(path)
	if err != nil {
		return err
	}
	if current == nil {
		current = make(map[string]rawState)
	}
	entry := current[loaded.Manifest.ID]
	if entry.Enabled == nil {
		entry.Enabled = boolPtr(loaded.State.Enabled)
	}
	entry.Trust = trust
	current[loaded.Manifest.ID] = entry
	return writeStateFile(path, current)
}

func writeStateFile(path string, states map[string]rawState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir plugin state dir: %w", err)
	}
	data, err := json.MarshalIndent(stateFile{Plugins: states}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal plugin state %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write plugin state %s: %w", path, err)
	}
	return nil
}

func statePathForScope(cwd, scope string) string {
	switch scope {
	case "user":
		return userStatePath()
	case "project", "builtin":
		return projectStatePath(cwd)
	default:
		return ""
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func projectPluginsDir(cwd string) string {
	return filepath.Join(cwd, config.ConfigDir, "plugins")
}

func projectStatePath(cwd string) string {
	return filepath.Join(cwd, config.ConfigDir, "plugins-state.json")
}

func userPluginsDir() string {
	dir := config.UserConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "plugins")
}

func userStatePath() string {
	dir := config.UserConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "plugins-state.json")
}
