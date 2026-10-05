package plugin

import (
	"maps"
	"slices"

	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/infra/config"
)

// Manifest describes a plugin package and the contributions it exports.
type Manifest struct {
	ID          string                      `json:"id"`
	Name        string                      `json:"name"`
	Version     string                      `json:"version"`
	Description string                      `json:"description,omitempty"`
	SkillsDir   string                      `json:"skillsDir,omitempty"`
	MCPServers  map[string]config.MCPServer `json:"mcpServers,omitempty"`
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

	bundled []skill.Spec // the builtin plugin's skills
}

// Contributions is the runtime-facing view of all enabled plugins.
type Contributions struct {
	Skills     []skill.Spec
	MCPServers map[string]config.MCPServer
}

// Catalog is the in-memory registry of discovered plugins.
type Catalog struct {
	plugins []Loaded
}

// Plugins returns a copy of all discovered plugins, including disabled ones.
func (c *Catalog) Plugins() []Loaded {
	return slices.Clone(c.plugins)
}

// Contributions collects what the enabled plugins contribute. No two of them
// contribute an MCP server of the same name: LoadAll rejects that.
func (c *Catalog) Contributions() Contributions {
	out := Contributions{MCPServers: make(map[string]config.MCPServer)}
	for _, p := range c.plugins {
		if !p.State.Enabled {
			continue
		}
		out.Skills = append(out.Skills, p.Skills()...)
		maps.Copy(out.MCPServers, p.mcpServers())
	}
	return out
}

// Skills loads the plugin's skills, marked with the source they come from:
// its scope if it is trusted, "remote" if not.
func (p Loaded) Skills() []skill.Spec {
	specs := slices.Clone(p.bundled)
	if dir := p.skillDir(); dir != "" {
		loaded, _ := skill.LoadDir(dir)
		specs = append(specs, loaded...)
	}
	source := "remote"
	if p.IsTrusted() {
		source = map[string]string{"builtin": "bundled", "project": "project", "user": "user"}[p.Scope]
	}
	for i := range specs {
		specs[i].Source = source
	}
	return specs
}

// mcpServers returns the plugin's MCP servers, if it is trusted to run them.
func (p Loaded) mcpServers() map[string]config.MCPServer {
	if !p.IsTrusted() {
		return nil
	}
	return p.Manifest.MCPServers
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

func (p Loaded) IsTrusted() bool {
	return IsTrusted(p.State.Trust)
}

// SkillCount returns the number of skills contributed by this plugin.
func (p Loaded) SkillCount() int {
	return len(p.Skills())
}

// MCPCount returns the number of MCP servers contributed by this plugin.
func (p Loaded) MCPCount() int {
	return len(p.Manifest.MCPServers)
}
