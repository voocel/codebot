package plugin

import (
	"github.com/voocel/codebot/internal/config"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/skill"
)

// Manifest describes a plugin package and the contributions it exports.
type Manifest struct {
	ID          string                            `json:"id"`
	Name        string                            `json:"name"`
	Version     string                            `json:"version"`
	Description string                            `json:"description,omitempty"`
	SkillsDir   string                            `json:"skillsDir,omitempty"`
	CommandsDir string                            `json:"commandsDir,omitempty"`
	MCPServers  map[string]mcpclient.ServerConfig `json:"mcpServers,omitempty"`
}

// State stores local operator decisions, not plugin-authored metadata.
type State struct {
	Enabled bool
	Trust   string
}

// Loaded is a validated plugin plus its effective local state.
type Loaded struct {
	Manifest Manifest
	State    State
	RootDir  string
	Scope    string // "builtin", "project", or "user"

	skillSpecs []skill.Spec
}

// SkillDir identifies a skill directory contributed by a plugin.
type SkillDir struct {
	Path   string
	Source string
}

// Contributions is the runtime-facing view of all enabled plugins.
type Contributions struct {
	SkillSpecs  []skill.Spec
	SkillDirs   []SkillDir
	CommandDirs []string
	MCPServers  map[string]mcpclient.ServerConfig
}

// Catalog is the in-memory registry of discovered plugins.
type Catalog struct {
	plugins []Loaded
}

// Plugins returns a copy of all discovered plugins, including disabled ones.
func (c *Catalog) Plugins() []Loaded {
	if c == nil || len(c.plugins) == 0 {
		return nil
	}
	out := make([]Loaded, len(c.plugins))
	copy(out, c.plugins)
	return out
}

// Contributions collects runtime contributions from enabled plugins.
func (c *Catalog) Contributions() Contributions {
	if c == nil {
		return Contributions{}
	}
	out := Contributions{
		MCPServers: make(map[string]mcpclient.ServerConfig),
	}
	for _, p := range c.plugins {
		if !p.State.Enabled {
			continue
		}
		if len(p.skillSpecs) > 0 {
			out.SkillSpecs = append(out.SkillSpecs, RuntimeSkillSpecs(p.Scope, p.State.Trust, p.skillSpecs)...)
		}
		if dir := p.skillDir(); dir != "" {
			out.SkillDirs = append(out.SkillDirs, SkillDir{
				Path:   dir,
				Source: RuntimeSkillSource(p.Scope, p.State.Trust),
			})
		}
		if dir := p.commandsDir(); dir != "" {
			out.CommandDirs = append(out.CommandDirs, dir)
		}
		for name, cfg := range AllowedMCPServers(p.State.Trust, p.Manifest.MCPServers) {
			if _, exists := out.MCPServers[name]; !exists {
				out.MCPServers[name] = cfg
			}
		}
	}
	if len(out.MCPServers) == 0 {
		out.MCPServers = nil
	}
	return out
}

func (p Loaded) skillDir() string {
	if p.Manifest.SkillsDir == "" {
		return ""
	}
	dir, err := resolveRelativeDir(p.RootDir, p.Manifest.SkillsDir)
	if err != nil {
		return ""
	}
	return dir
}

func (p Loaded) commandsDir() string {
	if p.Manifest.CommandsDir == "" {
		return ""
	}
	dir, err := resolveRelativeDir(p.RootDir, p.Manifest.CommandsDir)
	if err != nil {
		return ""
	}
	return dir
}

func (p Loaded) IsTrusted() bool {
	return IsTrusted(p.State.Trust)
}

// SkillCount returns the number of skills contributed by this plugin.
func (p Loaded) SkillCount() int {
	count := len(p.skillSpecs)
	if p.skillDir() != "" {
		count += len(skill.LoadFromDir(p.skillDir(), "plugin"))
	}
	return count
}

// CommandCount returns the number of command roots contributed by this plugin.
func (p Loaded) CommandCount() int {
	dir := p.commandsDir()
	if dir == "" {
		return 0
	}
	return len(config.LoadCommandsFromDir(dir, "plugin"))
}

// MCPCount returns the number of MCP servers contributed by this plugin.
func (p Loaded) MCPCount() int {
	return len(p.Manifest.MCPServers)
}
