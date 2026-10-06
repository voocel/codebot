// Package plugin reads plugins in the Agent Plugins 1.0 format and fetches
// them from git. A plugin is a directory: plugin.json naming it, skills in
// skills/, MCP servers in mcp.json. See
// https://github.com/agentplugins/agent-plugins-spec.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/agent/subagent"
	"github.com/voocel/codebot/internal/extension/hooks"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/regular"
)

const (
	pluginSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	mcpSchema    = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
)

// Namespace is codebot's among a plugin's extensions. It holds what the
// format leaves to each client: the plugin's hooks, as settings give them,
// and the directory of its agents.
//
//	"extensions": {"io.github.voocel.codebot": {
//		"hooks": {"PreToolUse": [{"type": "command", "command": "\"$PLUGIN_ROOT\"/bin/guard", "matcher": "bash"}]},
//		"agents": "./agents"
//	}}
const Namespace = "io.github.voocel.codebot"

// Manifest is what plugin.json says of a plugin.
type Manifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Author      *Author  `json:"author"`
	Homepage    string   `json:"homepage"`
	Repository  string   `json:"repository"`
	License     string   `json:"license"`
	Keywords    []string `json:"keywords"`
}

// Author is a plugin's author.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	URL   string `json:"url"`
}

// Plugin is a plugin read from its directory.
type Plugin struct {
	Manifest
	// Root is the plugin's directory, symlinks resolved.
	Root string
	// Data is the directory its MCP servers keep data in across updates,
	// PLUGIN_DATA; it may not exist yet.
	Data string
	// Skills are its skills, named as it names them, frozen: they run as
	// they were read.
	Skills []skill.Spec
	// MCP are its MCP servers, named as it names them, ready to run.
	MCP map[string]config.MCPServer
	// Hooks are its hooks, by event; command hooks get PLUGIN_ROOT and
	// PLUGIN_DATA in their environment.
	Hooks config.HooksConfig
	// Agents are its agents, named as it names them.
	Agents []subagent.AgentDefinition

	ext json.RawMessage // its extension in codebot's namespace
}

// Read reads the plugin in dir, its data kept under dataRoot. A manifest
// that breaks the format rejects the whole plugin, an error; a broken skill
// or MCP configuration is left out and reported among problems.
func Read(dir, dataRoot string) (p *Plugin, problems []error, err error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, nil, err
	}
	p = &Plugin{Root: root}
	if problems, err = p.readManifest(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dir, err)
	}
	p.Data = filepath.Join(dataRoot, p.Name)
	problems = append(problems, p.readSkills()...)
	problems = append(problems, p.readMCP()...)
	problems = append(problems, p.readExtension()...)
	return p, problems, nil
}

var reName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// ValidName reports whether name is a plugin's name: up to 64 lower-case
// letters, digits, "-" and ".", starting and ending with a letter or digit,
// with no "--" or "..". It holds no "_" or ":", which tell it apart where
// it names what the plugin brings.
func ValidName(name string) bool {
	return len(name) <= 64 && reName.MatchString(name) && !strings.Contains(name, "--") && !strings.Contains(name, "..")
}

// readManifest reads plugin.json. Fields the format does not know, and a
// malformed extensions, are reported and ignored; any other violation is
// an error.
func (p *Plugin) readManifest() (problems []error, err error) {
	file, err := p.inside(filepath.Join(p.Root, "plugin.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("no plugin.json: not an Agent Plugins plugin")
	}
	if err != nil {
		return nil, err
	}
	data, err := regular.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	known := []string{"$schema", "name", "version", "description", "author", "homepage", "repository", "license", "keywords", "extensions"}
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		if !slices.Contains(known, k) {
			problems = append(problems, fmt.Errorf("plugin.json: unknown field %q, ignored", k))
			delete(fields, k)
		}
	}
	var schema string
	if err := json.Unmarshal(fields["$schema"], &schema); err != nil || schema != pluginSchema {
		return nil, fmt.Errorf("plugin.json: $schema must be %s", pluginSchema)
	}
	if ext, ok := fields["extensions"]; ok {
		// Namespaces codebot does not implement are not looked into.
		var namespaces map[string]json.RawMessage
		if err := json.Unmarshal(ext, &namespaces); err != nil || namespaces == nil {
			problems = append(problems, errors.New("plugin.json: extensions is not an object, ignored"))
		} else {
			for ns, v := range namespaces {
				if !bytes.HasPrefix(bytes.TrimSpace(v), []byte("{")) {
					return nil, fmt.Errorf("plugin.json: extensions.%s is not an object", ns)
				}
			}
			p.ext = namespaces[Namespace]
		}
		delete(fields, "extensions")
	}
	delete(fields, "$schema")
	rest, _ := json.Marshal(fields)
	dec := json.NewDecoder(bytes.NewReader(rest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p.Manifest); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	if !ValidName(p.Name) {
		return nil, fmt.Errorf("plugin.json: invalid name %q", p.Name)
	}
	return problems, nil
}

// readSkills reads the skills: each directory right under skills/ holding
// a SKILL.md.
func (p *Plugin) readSkills() (problems []error) {
	dir := filepath.Join(p.Root, "skills")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []error{err}
	}
	if _, err := p.inside(dir); err != nil {
		return []error{err}
	}
	for _, e := range entries {
		file, err := p.inside(filepath.Join(dir, e.Name(), "SKILL.md"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			problems = append(problems, err)
			continue
		}
		if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
			continue
		}
		spec, err := skill.LoadFile(file, e.Name())
		if err == nil {
			spec, err = spec.Freeze()
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", file, err))
			continue
		}
		p.Skills = append(p.Skills, spec)
	}
	return problems
}

// inside returns path with symlinks resolved, failing where it resolves
// outside the plugin.
func (p *Plugin) inside(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !within(p.Root, real) {
		return "", fmt.Errorf("%s leads outside the plugin", path)
	}
	return real, nil
}

// within reports whether path is dir or under it, both clean.
func within(dir, path string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// readExtension reads what codebot's namespace brings. A namespace that
// breaks its format is left out whole, a broken hook or agent only itself.
func (p *Plugin) readExtension() (problems []error) {
	if p.ext == nil {
		return nil
	}
	var ext struct {
		Hooks  map[string][]json.RawMessage `json:"hooks"`
		Agents string                       `json:"agents"`
	}
	dec := json.NewDecoder(bytes.NewReader(p.ext))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ext); err != nil {
		return []error{fmt.Errorf("plugin.json: extensions.%s: %w; left out", Namespace, err)}
	}
	for _, event := range slices.Sorted(maps.Keys(ext.Hooks)) {
		for i, raw := range ext.Hooks[event] {
			he, err := p.hook(event, raw)
			if err != nil {
				problems = append(problems, fmt.Errorf("plugin.json: hooks.%s[%d]: %w", event, i, err))
				continue
			}
			if p.Hooks == nil {
				p.Hooks = config.HooksConfig{}
			}
			p.Hooks[event] = append(p.Hooks[event], he)
		}
	}
	if ext.Agents != "" {
		problems = append(problems, p.readAgents(ext.Agents)...)
	}
	return problems
}

// hook reads a hook of event. Its command runs as written, PLUGIN_ROOT and
// PLUGIN_DATA in its environment for the shell to expand; an http hook
// calls https alone, as a remote MCP server does.
func (p *Plugin) hook(event string, raw json.RawMessage) (config.HookEntry, error) {
	var he config.HookEntry
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&he); err != nil {
		return he, err
	}
	if err := hooks.Check(event, he); err != nil {
		return he, err
	}
	if he.Type == "http" {
		if err := checkURL(he.URL); err != nil {
			return he, err
		}
		if err := checkHeaders(he.Headers); err != nil {
			return he, err
		}
	}
	he.Env = map[string]string{"PLUGIN_ROOT": p.Root, "PLUGIN_DATA": p.Data}
	return he, nil
}

// readAgents reads the agents in dir, a path in the plugin starting "./":
// each *.md file in it.
func (p *Plugin) readAgents(dir string) (problems []error) {
	rel, ok := strings.CutPrefix(dir, "./")
	if !ok {
		return []error{fmt.Errorf("plugin.json: agents %q does not start ./", dir)}
	}
	real, err := p.inside(filepath.Join(p.Root, filepath.FromSlash(rel)))
	if err != nil {
		return []error{err}
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return []error{err}
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		file, err := p.inside(filepath.Join(real, e.Name()))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		def, err := subagent.LoadFile(file)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", file, err))
			continue
		}
		p.Agents = append(p.Agents, def)
	}
	return problems
}
