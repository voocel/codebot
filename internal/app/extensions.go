package app

import (
	"context"
	"fmt"
	"maps"
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

// Trust is how the user stands on the project's surface: see
// extension.Trust.
type Trust = extension.Trust

// extensions are the extensions loaded, with what the conversations take
// of them.
type extensions struct {
	*extension.Set
	skills *skill.Catalog
}

// load loads the extensions and the settings under layers, as the user
// agreed to what runs.
func (a *App) load(layers config.Layers) (*extensions, config.Resolved, error) {
	consents, err := extension.ReadConsents()
	if err != nil {
		return nil, config.Resolved{}, err
	}
	a.mu.Lock()
	if a.session != nil {
		consents.Projects = maps.Clone(consents.Projects)
		if consents.Projects == nil {
			consents.Projects = map[string]extension.Consent{}
		}
		consents.Projects[a.root] = *a.session
	}
	a.mu.Unlock()
	set := extension.Load(extension.Options{Cwd: a.cwd, Layers: layers, Consents: consents, PluginDirs: a.opts.PluginDirs, TrustAll: a.opts.Trust})
	settings, err := layers.Resolve(a.cwd, set.Granted)
	if err != nil {
		return nil, config.Resolved{}, err
	}
	return &extensions{Set: set, skills: skill.NewCatalog(set.Skills)}, settings, nil
}

// Extensions returns the extensions in effect.
func (a *App) Extensions() *Extensions { return a.ext.Load().Set }

// Trust returns how the user stands on the project's surface.
func (a *App) Trust() Trust { return a.Extensions().Trust }

// SetTrust decides on the project's surface as the user was shown it, a
// Trust's: to trust it or not, for this session or, with remember, from now
// on. What the surface has that the user was not shown waits to be asked
// about. The extensions reload.
func (a *App) SetTrust(ctx context.Context, shown Surface, trusted, remember bool) (ReloadReport, error) {
	c := extension.Consent{Surface: shown}
	if !trusted {
		c = extension.Consent{Denied: true}
	}
	if remember {
		if err := extension.EditConsents(func(cs *extension.Consents) { cs.Projects[a.root] = c }); err != nil {
			return ReloadReport{}, err
		}
	}
	a.mu.Lock()
	a.session = nil
	if !remember {
		a.session = &c
	}
	a.mu.Unlock()
	return a.refresh(ctx)
}

// agreeToProject adds it to what the user agreed to of the project, unless
// they do not trust it: they wrote it there themselves.
func (a *App) agreeToProject(it extension.Item) error {
	agree := func(c extension.Consent) extension.Consent {
		if !c.Denied {
			c.Surface = c.Surface.With(it)
		}
		return c
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != nil {
		*a.session = agree(*a.session)
		return nil
	}
	return extension.EditConsents(func(c *extension.Consents) { c.Projects[a.root] = agree(c.Projects[a.root]) })
}

func (a *App) skillCatalog() *skill.Catalog { return a.ext.Load().skills }

// ReloadReport says what is in effect once the extensions reload.
type ReloadReport struct {
	Skills  int
	Plugins int // on
	MCP     MCPReport
}

// Reload rereads the extensions and the permission rules, puts them in
// effect and restarts the MCP servers, those of a plugin under development
// among them; the conversation picks them up from its next run. The other
// settings take effect as codebot restarts.
func (a *App) Reload(ctx context.Context) (ReloadReport, error) { return a.apply(ctx, true) }

// refresh rereads the extensions and puts them in effect, reconnecting the
// MCP servers that changed alone.
func (a *App) refresh(ctx context.Context) (ReloadReport, error) { return a.apply(ctx, false) }

func (a *App) apply(ctx context.Context, restart bool) (ReloadReport, error) {
	if err := a.reload(); err != nil {
		return ReloadReport{}, err
	}
	r := ReloadReport{MCP: a.connectMCP(ctx, restart)}
	ext := a.Extensions()
	r.Skills = len(ext.Skills)
	for _, pl := range ext.Plugins {
		if pl.State == extension.PluginOn {
			r.Plugins++
		}
	}
	return r, nil
}

// Connect connects the MCP servers in effect. Frontends call it once
// subscribed.
func (a *App) Connect(ctx context.Context) MCPReport { return a.connectMCP(ctx, false) }

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
// longer; with restart, it reconnects all of them. Their tools join the
// conversation from its next run.
func (a *App) connectMCP(ctx context.Context, restart bool) MCPReport {
	a.connecting.Lock()
	defer a.connecting.Unlock()
	if restart {
		a.mcp.Configure(ctx, nil)
	}
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
