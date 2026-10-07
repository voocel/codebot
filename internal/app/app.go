// Package app assembles codebot. App holds process-wide state (settings,
// models, permissions, extensions, MCP) and the current Conversation, which
// holds per-session state. Frontends use only these two types.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/agent/tools"
	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/extension/mcp"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/infra/telemetry"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/internal/session/storage"
	"github.com/voocel/codebot/internal/workspace/worktree"
)

type ModelFactory func(provider.ModelSpec) (agentcore.Model, error)

type Options struct {
	Cwd  string
	Mode interact.Mode
	// Resume is a session ID; "" starts a new session.
	Resume string
	UI     interact.UI
	// Interactive enables ask_user; headless frontends have no one to ask.
	Interactive bool
	// CacheTTL is passed as litellm.CacheControl.TTL. Use "1h" when a person
	// is in the loop: they pause longer than the default five minutes.
	CacheTTL string
	// FS backs read, write and edit; nil means the local filesystem.
	FS agentcoretools.FS
	// NewModel overrides how models are built; nil uses litellm.
	NewModel ModelFactory
	// Trust trusts the project and the plugins it declares for this process,
	// ignoring saved consent.
	Trust bool
	// PluginDirs are local plugins under development, loaded for this
	// process only with everything they run allowed.
	PluginDirs []string
}

type App struct {
	opts Options
	cwd  string
	// settings stay as Boot loaded them, except the model selection
	// (Provider, Model, ReasoningEffort), which changes under mu.
	settings    config.Resolved
	models      *provider.Models
	newModel    ModelFactory
	sessions    *storage.Manager
	permissions *permission.Engine
	modeMu      sync.Mutex // orders mode switches with their events
	mcp         *mcp.Manager
	usage       *skill.UsageTracker
	tracer      *telemetry.Tracer
	shutdown    func(context.Context) error
	events      broadcaster
	// ext and offered are swapped whole on reload and refresh. They are read
	// without mu so a Conversation holding its own lock never waits on the
	// App's.
	ext     atomic.Pointer[extensions]
	offered atomic.Pointer[mcpOffer]
	// reloading and connecting serialize reloads and MCP connects, so the
	// configuration read last is the one in effect.
	reloading, connecting sync.Mutex

	mu          sync.Mutex
	current     *Conversation
	unsubscribe func()
}

func Boot(opts Options) (*App, error) {
	cwd := opts.Cwd
	if config.NeedsSetup() {
		return nil, fmt.Errorf("no configuration found; run codebot -setup to create one")
	}
	layers, err := config.Load(cwd)
	if err != nil {
		return nil, err
	}
	a := &App{
		opts:     opts,
		cwd:      cwd,
		models:   provider.NewModels(),
		newModel: opts.NewModel,
		sessions: storage.NewManager(config.SessionsDir(cwd)),
		shutdown: func(context.Context) error { return nil },
	}
	ext, settings, err := a.load(layers)
	if err != nil {
		return nil, err
	}
	if err := checkProviderSetup(settings); err != nil {
		return nil, err
	}
	a.settings = settings
	a.ext.Store(ext)
	a.offered.Store(&mcpOffer{})
	if a.newModel == nil {
		a.models.Refresh(config.UserConfigDir())
		observer, tracer, shutdown, err := telemetry.Setup(context.Background(), settings.Telemetry)
		if err != nil {
			return nil, err
		}
		var clientOpts []litellm.ClientOption
		if observer != nil {
			clientOpts = append(clientOpts, litellm.WithObservers(observer))
		}
		a.newModel = provider.NewModelFactory(a.models, clientOpts...)
		a.tracer, a.shutdown = tracer, shutdown
	}
	if a.usage, err = skill.NewUsageTracker(filepath.Join(config.UserConfigDir(), "skill-usage.json")); err != nil {
		return nil, fmt.Errorf("skill usage: %w", err)
	}
	if a.permissions, err = newPermissionEngine(cwd, opts, settings, ext.Set); err != nil {
		return nil, err
	}
	a.mcp = mcp.NewManager(func() { go a.refreshMCP() })

	if _, err := a.Open(opts.Resume); err != nil {
		return nil, err
	}
	go tools.CleanOldOutputs(config.SessionsDir(cwd))
	go cleanWorktreeOrphans(cwd)
	go sweepPluginCache()
	return a, nil
}

// Open replaces the current conversation with session id, or with a new
// session when id is "". On error the current conversation stays open.
func (a *App) Open(id string) (*Conversation, error) {
	var (
		store *storage.Store
		state storage.State
		err   error
	)
	if id == "" {
		store, err = a.sessions.Create(a.cwd)
	} else {
		store, state, err = a.sessions.Open(id)
	}
	if err != nil {
		return nil, err
	}
	c, err := openConversation(a, store, state)
	if err != nil {
		store.Close()
		return nil, err
	}

	a.mu.Lock()
	prev, unsubscribe := a.current, a.unsubscribe
	a.current = c
	a.unsubscribe = c.session.Subscribe(func(ev session.Event) { a.events.publish(Event{Kind: SessionEvent, Session: ev, Conversation: c}) })
	a.mu.Unlock()
	if prev != nil {
		unsubscribe()
		prev.close()
	}
	c.start()
	a.events.publish(Event{Kind: Opened, Conversation: c})
	return c, nil
}

func (a *App) Current() *Conversation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

type SessionInfo = storage.SessionInfo

// Sessions returns the workspace's sessions, newest first.
func (a *App) Sessions() ([]SessionInfo, error) { return a.sessions.List() }

// Subscribe delivers App events and the current conversation's events,
// following it across Open, one at a time in order. fn runs on the goroutine
// that caused the event. It must not cause another event (switch the mode,
// open a conversation) or wait on the conversation.
func (a *App) Subscribe(fn func(Event)) (unsubscribe func()) { return a.events.subscribe(fn) }

func (a *App) Close() {
	a.mu.Lock()
	c, unsubscribe := a.current, a.unsubscribe
	a.current = nil
	a.mu.Unlock()
	if c != nil {
		unsubscribe()
		c.close()
	}
	a.mcp.Close()
	// Bound the final span flush so a hung OTLP backend can't block exit.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.shutdown(ctx)
}

func (a *App) Cwd() string { return a.cwd }

func (a *App) Settings() config.Resolved {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

func (a *App) defaultModel() storage.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return storage.Model{Provider: a.settings.Provider, Model: a.settings.Model, Effort: a.settings.ReasoningEffort}
}

func (a *App) rememberModel(prov, name, effort string) error {
	if err := config.PatchUserSettings(config.Settings{Provider: &prov, Model: &name, ReasoningEffort: &effort}); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.Provider, a.settings.Model, a.settings.ReasoningEffort = prov, name, effort
	return nil
}

func (a *App) Mode() interact.Mode { return a.permissions.Mode() }

func (a *App) SetMode(m interact.Mode) {
	a.modeMu.Lock()
	defer a.modeMu.Unlock()
	if a.permissions.Mode() == m {
		return
	}
	a.permissions.SetMode(m)
	a.events.publish(Event{Kind: ModeChanged, Mode: m})
}

func newPermissionEngine(cwd string, opts Options, settings config.Resolved, ext *extension.Set) (*permission.Engine, error) {
	rules, err := permission.ParseRuleSet(settings.Permissions.Allow, settings.Permissions.Deny)
	if err != nil {
		return nil, fmt.Errorf("parse permission rules: %w", err)
	}
	engine, err := permission.NewEngine(permission.Config{
		Cwd:     cwd,
		Mode:    opts.Mode,
		Rules:   rules,
		Roots:   filesystemRoots(cwd, settings, ext),
		UI:      opts.UI,
		OnAudit: auditor(config.AuditLogPath()),
	})
	if err != nil {
		return nil, fmt.Errorf("permission engine: %w", err)
	}
	return engine, nil
}

// filesystemRoots are where tools may read and write without asking.
func filesystemRoots(cwd string, settings config.Resolved, ext *extension.Set) permission.FilesystemRoots {
	memoryDir := config.MemoryDir(cwd)
	// Local plugin directories hold code that runs when they load.
	var plugins []string
	for _, pl := range ext.Plugins {
		if pl.Src.Dir != "" {
			plugins = append(plugins, pl.Src.Dir)
		}
	}
	// Sandbox worktrees belong to the workspace even if the configured roots
	// leave them out.
	worktrees := worktree.Root(cwd)
	return permission.FilesystemRoots{
		ReadRoots:  append(slices.Clone(settings.Permissions.ReadRoots), config.SessionsDir(cwd), worktrees),
		WriteRoots: append(slices.Clone(settings.Permissions.WriteRoots), worktrees),
		// Auto-memory lives outside the workspace but codebot manages it, so
		// it skips the outside-roots prompt.
		InternalReadable: []string{memoryDir},
		InternalWritable: []string{memoryDir},
		Protected:        plugins,
	}
}

func auditor(path string) func(permission.AuditEntry) {
	var mu sync.Mutex
	return func(e permission.AuditEntry) {
		entry := map[string]any{
			"time":       e.Time.Format(time.RFC3339Nano),
			"mode":       string(e.Mode),
			"tool":       e.Tool,
			"capability": string(e.Capability),
			"summary":    e.Summary,
			"decision":   e.Decision,
			"allow":      e.Allow,
		}
		if e.Reason != "" {
			entry["reason"] = e.Reason
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return
		}
		data = append(data, '\n')

		mu.Lock()
		defer mu.Unlock()
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Write(data)
	}
}

// checkProviderSetup runs after the first-run wizard, so anything missing
// is an error.
func checkProviderSetup(settings config.Resolved) error {
	if pc, ok := settings.Providers[settings.Provider]; !ok || !pc.HasCredentials() {
		return fmt.Errorf("configuration error: settings.provider=%q is missing or not configured in settings.json", settings.Provider)
	}
	if settings.Model == "" {
		return fmt.Errorf("configuration error: model is not set in settings.json")
	}
	return nil
}
