package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/mcp-sdk-go/auth"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/extension/mcp"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/printable"
)

type Extensions = extension.Set

type Surface = extension.Surface

type Skill = skill.Spec

type Trust = extension.Trust

type extensions struct {
	*extension.Set
	skills *skill.Catalog
}

func (a *App) load(layers config.Layers) (*extensions, config.Resolved, error) {
	consents, err := extension.ReadConsents()
	if err != nil {
		return nil, config.Resolved{}, err
	}
	set := extension.Load(extension.Options{Cwd: a.cwd, Layers: layers, Consents: consents, PluginDirs: a.opts.PluginDirs, TrustAll: a.opts.Trust})
	settings, err := layers.Resolve(a.cwd, set.Granted)
	if err != nil {
		return nil, config.Resolved{}, err
	}
	return &extensions{Set: set, skills: skill.NewCatalog(set.Skills)}, settings, nil
}

func (a *App) Extensions() *Extensions { return a.ext.Load().Set }

// Printable escapes what a terminal would act on rather than display, for
// text read from files.
func Printable(s string) string { return printable.Escape(s) }

func (a *App) Trust() Trust { return a.Extensions().Trust }

// SetTrust records the user's decision on the shown part of the project's
// surface: agreed is accepted and the rest of shown is declined. The
// extensions then reload.
func (a *App) SetTrust(ctx context.Context, shown, agreed Surface) (ReloadReport, error) {
	surface := a.Trust().Surface
	if err := a.decideProject(func(c extension.Consent) extension.Consent { return c.Decided(surface, shown, agreed) }); err != nil {
		return ReloadReport{}, err
	}
	return a.refresh(ctx)
}

// DenyTrust marks the project untrusted: nothing it runs takes effect and
// the user is not asked again. The extensions then reload.
func (a *App) DenyTrust(ctx context.Context) (ReloadReport, error) {
	if err := a.decideProject(func(extension.Consent) extension.Consent { return extension.Consent{Denied: true} }); err != nil {
		return ReloadReport{}, err
	}
	return a.refresh(ctx)
}

// agreeToProject records consent for an item the user added to the project
// themselves, unless the project is denied.
func (a *App) agreeToProject(it extension.Item) error {
	return a.decideProject(func(c extension.Consent) extension.Consent {
		if !c.Denied {
			c.Surface, c.Declined = c.Surface.With(it), c.Declined.Missing(Surface{it})
		}
		return c
	})
}

func (a *App) decideProject(decide func(extension.Consent) extension.Consent) error {
	root := a.Trust().Root
	return extension.EditConsents(func(cs *extension.Consents) { cs.Projects[root] = decide(cs.Projects[root]) })
}

func (a *App) skillCatalog() *skill.Catalog { return a.ext.Load().skills }

type ReloadReport struct {
	Skills  int
	Plugins int // plugins that are on
	MCP     MCPReport
}

// Reload rereads extensions and permission rules and restarts all MCP
// servers, including those of plugins under development. The conversation
// picks them up on its next run; other settings need a codebot restart.
func (a *App) Reload(ctx context.Context) (ReloadReport, error) { return a.apply(ctx, true) }

// refresh is Reload without restarting unchanged MCP servers.
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

// Connect is called by frontends once they have subscribed.
func (a *App) Connect(ctx context.Context) MCPReport { return a.connectMCP(ctx, false) }

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

type MCPReport struct {
	Servers   int
	Connected int
	Tools     int
	Errors    []string
	Login     []string // servers that want an OAuth login: /mcp login
}

// connectMCP also disconnects servers no longer configured; restart
// reconnects all of them.
func (a *App) connectMCP(ctx context.Context, restart bool) MCPReport {
	a.connecting.Lock()
	defer a.connecting.Unlock()
	if restart {
		a.mcp.Configure(ctx, nil)
	}
	servers := a.Extensions().MCPConfig()
	failures := a.mcp.Configure(ctx, servers)
	report := MCPReport{Servers: len(servers), Connected: len(servers) - len(failures), Tools: a.refreshMCP()}
	for _, f := range failures {
		if f.Login {
			report.Login = append(report.Login, f.Server)
		} else {
			report.Errors = append(report.Errors, f.Server+": "+f.Err.Error())
		}
	}
	slices.Sort(report.Login)
	return report
}

// MCPLogin is an OAuth login waiting for the user to authorize in a browser.
type MCPLogin struct {
	URL   string // the page to open
	app   *App
	login *auth.Login
}

// LoginMCP starts an OAuth login to the MCP server name.
func (a *App) LoginMCP(ctx context.Context, name string) (*MCPLogin, error) {
	l, err := a.mcp.Login(ctx, name)
	if err != nil {
		return nil, err
	}
	return &MCPLogin{URL: l.URL, app: a, login: l}, nil
}

// Wait waits for the user to authorize, then connects the servers that
// failed.
func (l *MCPLogin) Wait(ctx context.Context) (MCPReport, error) {
	if err := l.login.Wait(ctx); err != nil {
		return MCPReport{}, err
	}
	return l.app.connectMCP(context.WithoutCancel(ctx), false), nil
}

// LogoutMCP forgets the server's OAuth token and connects it again without.
func (a *App) LogoutMCP(ctx context.Context, name string) (MCPReport, error) {
	if err := a.mcp.Logout(name); err != nil {
		return MCPReport{}, err
	}
	return a.connectMCP(ctx, false), nil
}

type mcpOffer struct {
	tools        []agentcore.Tool
	permissions  map[string]permission.Metadata
	instructions string
}

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

// toolPermission returns an MCP tool's self-declared permission metadata;
// zero for other tools.
func (a *App) toolPermission(name string) permission.Metadata {
	return a.offered.Load().permissions[name]
}

type MCPServer = mcp.ServerStatus

func (a *App) MCPStatus(ctx context.Context) []MCPServer { return a.mcp.Status(ctx) }
