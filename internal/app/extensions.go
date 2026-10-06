package app

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/extension/mcp"
	"github.com/voocel/codebot/internal/infra/config"
)

// Extensions are the extensions in effect; see the extension package.
type Extensions = extension.Set

// Surface is what of a project takes effect only once the user trusts it.
type Surface = extension.Surface

// Skill is a loaded skill.
type Skill = skill.Spec

// Trust is whether the project's surface is in effect.
type Trust struct {
	// Root is the project's, "" where there is none: in the home directory.
	Root    string
	Trusted bool
	// Surface is all of the project's surface.
	Surface Surface
	// Ask is what of the surface the user has yet to decide on: all of it
	// before they first do, then what was added since they trusted it.
	// Empty, there is nothing to ask.
	Ask Surface
}

// Held reports whether some of the project's surface is not in effect.
func (t Trust) Held() bool { return !t.Trusted && len(t.Surface) > 0 }

// extensions are the extensions loaded, with what the conversations take
// of them.
type extensions struct {
	*extension.Set
	trust  Trust
	skills *skill.Catalog
}

// load loads the extensions and the settings under layers, the project's
// surface taking effect if the user trusts it.
func (a *App) load(layers config.Layers) (*extensions, config.Resolved, error) {
	ws, err := extension.ReadWorkspace(a.decisions())
	if err != nil {
		return nil, config.Resolved{}, err
	}
	decision := ws.Trust
	a.mu.Lock()
	if a.decision != nil {
		decision = a.decision
	}
	a.mu.Unlock()

	trust := Trust{Root: layers.Root}
	set := extension.Load(extension.Options{Cwd: a.cwd, Layers: layers, Disabled: ws.Disabled, Trust: func(s extension.Surface) bool {
		trust.Surface = s
		switch {
		case a.opts.Trust:
			trust.Trusted = true
		case decision == nil:
			trust.Ask = s
		case decision.Trusted:
			trust.Ask = s.Missing(decision.Surface)
			trust.Trusted = len(trust.Ask) == 0
		}
		return trust.Trusted
	}})
	settings, err := layers.Resolve(a.cwd, set.Trusted)
	if err != nil {
		return nil, config.Resolved{}, err
	}
	return &extensions{Set: set, trust: trust, skills: skill.NewCatalog(set.Skills)}, settings, nil
}

// Extensions returns the extensions in effect.
func (a *App) Extensions() *Extensions { return a.ext.Load().Set }

// Trust returns whether the project's surface is in effect.
func (a *App) Trust() Trust { return a.ext.Load().trust }

// SetTrust decides on the project's surface as the user was shown it, a
// Trust's: to trust it or not, for this session or, with remember, from now
// on. What the surface has that the user was not shown waits to be asked
// about. The extensions reload.
func (a *App) SetTrust(ctx context.Context, shown Surface, trusted, remember bool) (ReloadReport, error) {
	d := extension.Decision{Trusted: trusted, Surface: shown}
	if remember {
		if err := extension.EditWorkspace(a.decisions(), func(w *extension.Workspace) { w.Trust = &d }); err != nil {
			return ReloadReport{}, err
		}
	}
	a.mu.Lock()
	// Remembered, the decision is the workspace's, which what the user
	// agrees to later extends.
	a.decision = nil
	if !remember {
		a.decision = &d
	}
	a.mu.Unlock()
	return a.Reload(ctx)
}

func (a *App) skillCatalog() *skill.Catalog { return a.ext.Load().skills }

// decisions is what the user's decisions about the project are kept under:
// its root, or where there is none, the directory codebot runs in.
func (a *App) decisions() string { return cmp.Or(a.root, a.cwd) }

// ReloadReport says what is in effect once the extensions reload and
// connect.
type ReloadReport struct {
	Skills  int
	Plugins int // on
	// Fetched are the sources of the plugins fetched, and FetchErrors why
	// others could not be.
	Fetched     []string
	FetchErrors []string
	MCP         MCPReport
}

// Reload reloads the extensions and the permission rules, then connects
// them; the conversation picks them up from its next run. A project whose
// surface grew holds what was added until the user trusts it: see Trust.
// The other settings take effect as codebot restarts.
func (a *App) Reload(ctx context.Context) (ReloadReport, error) {
	if err := a.reload(); err != nil {
		return ReloadReport{}, err
	}
	return a.Connect(ctx), nil
}

// Connect brings up what the extensions read need beyond reading: it
// fetches the plugins missing that need no asking, which are those locked
// at a commit the user agreed to and a trusted project's new ones, reloads
// if it fetched any, and connects the MCP servers. Frontends call it once
// subscribed; Reload calls it after.
func (a *App) Connect(ctx context.Context) ReloadReport {
	var r ReloadReport
	r.Fetched, r.FetchErrors = a.fetchPlugins(ctx)
	r.MCP = a.connectMCP(ctx)
	ext := a.Extensions()
	r.Skills = len(ext.Skills)
	for _, pl := range ext.Plugins {
		if pl.State == extension.PluginOn {
			r.Plugins++
		}
	}
	return r
}

// reload reads the extensions and the permission rules afresh and puts
// them in effect.
func (a *App) reload() error {
	a.reloading.Lock()
	defer a.reloading.Unlock()
	layers, err := config.Load(a.cwd)
	if err != nil {
		return err
	}
	ext, settings, err := a.load(layers)
	if err != nil {
		return err
	}
	rules, err := permission.ParseRuleSet(settings.Permissions.Allow, settings.Permissions.Deny)
	if err != nil {
		return fmt.Errorf("parse permission rules: %w", err)
	}
	a.permissions.Configure(rules, filesystemRoots(a.cwd, settings, ext.Set))
	a.ext.Store(ext)
	if c := a.Current(); c != nil {
		c.Reload()
	}
	a.events.publish(Event{Kind: Reloaded})
	return nil
}

// MCPReport summarizes connecting MCP servers.
type MCPReport struct {
	Servers   int
	Connected int
	Tools     int
	Errors    []string
}

// connectMCP connects the MCP servers in effect, and disconnects those no
// longer. Their tools join the conversation from its next run.
func (a *App) connectMCP(ctx context.Context) MCPReport {
	a.connecting.Lock()
	defer a.connecting.Unlock()
	servers := a.Extensions().MCPConfig()
	errs := a.mcp.Configure(ctx, servers)
	report := MCPReport{Servers: len(servers), Connected: len(servers) - len(errs), Tools: a.refreshMCP()}
	for _, err := range errs {
		report.Errors = append(report.Errors, err.Error())
	}
	return report
}

// mcpOffer is what the connected MCP servers offer.
type mcpOffer struct {
	tools        []agentcore.Tool
	permissions  map[string]permission.Metadata
	instructions string
}

// refreshMCP reloads what the MCP servers offer into the conversation and
// returns the number of tools.
func (a *App) refreshMCP() int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, perms := a.mcp.Tools(ctx)
	a.offered.Store(&mcpOffer{tools: tools, permissions: perms, instructions: strings.Join(a.mcp.Instructions(), "\n\n")})
	if c := a.Current(); c != nil {
		c.mcpChanged()
	}
	a.events.publish(Event{Kind: MCPChanged})
	return len(tools)
}

// toolPermission is how the permission engine sees a tool that classifies
// itself, an MCP tool; zero for the others.
func (a *App) toolPermission(name string) permission.Metadata {
	return a.offered.Load().permissions[name]
}

// MCPServer is the state of a configured MCP server.
type MCPServer = mcp.ServerStatus

// MCPStatus reports each configured MCP server.
func (a *App) MCPStatus(ctx context.Context) []MCPServer { return a.mcp.Status(ctx) }
