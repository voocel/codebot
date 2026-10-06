// Package app assembles codebot. An App holds what lives as long as the
// process — settings, models, permissions, extensions and MCP — and the
// current Conversation, which holds what lives as long as one session.
// Frontends talk to these two types only.
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

// ModelFactory builds a model.
type ModelFactory func(provider.ModelSpec) (agentcore.Model, error)

// Options configures Boot.
type Options struct {
	Cwd  string // the workspace
	Mode interact.Mode
	// Resume opens this session; "" starts a new one.
	Resume string
	// UI is how the agent reaches the user.
	UI interact.UI
	// Interactive offers the model ask_user; headless frontends have no one
	// to ask.
	Interactive bool
	// CacheTTL is how long the prompt cache keeps the conversation, as
	// litellm.CacheControl.TTL: "1h" where turns wait on a person, who
	// pauses longer than the vendors' default five minutes.
	CacheTTL string
	// FS is the file backend for read, write and edit; nil means the local
	// filesystem.
	FS agentcoretools.FS
	// NewModel overrides how models are built; nil uses litellm.
	NewModel ModelFactory
	// Trust trusts the project for this process, whatever the user decided:
	// what it and the plugins it declares run takes effect.
	Trust bool
	// PluginDirs are plugins to load for this process alone, what they run
	// agreed to: directories under development.
	PluginDirs []string
}

// App is the process-wide state. See the package documentation.
type App struct {
	opts Options
	cwd  string
	root string // the project's, "" for none; see config.ProjectRoot
	// settings are as Boot loaded them, but for the model selection
	// (Provider, Model, ReasoningEffort), which SetModel changes under mu.
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
	// ext and offered are replaced whole on reload and refresh. They are
	// read without mu, so that a Conversation holding its own lock never
	// waits for the App's.
	ext     atomic.Pointer[extensions]
	offered atomic.Pointer[mcpOffer]
	// reloading and connecting order reloads and connecting MCP servers, so
	// the configuration last read is the one in effect.
	reloading, connecting sync.Mutex

	mu sync.Mutex
	// session is what the user agreed to of the project for this session,
	// over what they keep; nil for none.
	session     *extension.Consent
	current     *Conversation
	unsubscribe func()
}

// Boot loads the configuration and opens the first conversation.
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
		root:     layers.Root,
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

// Open closes the current conversation and opens the session id, or a new
// one when id is "". On error the current conversation stays open.
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
	a.unsubscribe = c.session.Subscribe(func(ev session.Event) { a.events.publish(Event{Kind: SessionEvent, Session: ev}) })
	a.mu.Unlock()
	if prev != nil {
		unsubscribe()
		prev.close()
	}
	c.start()
	a.events.publish(Event{Kind: Opened, Conversation: c})
	return c, nil
}

// Current returns the open conversation.
func (a *App) Current() *Conversation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// SessionInfo describes a saved session.
type SessionInfo = storage.SessionInfo

// Sessions lists the sessions of the workspace, newest first.
func (a *App) Sessions() ([]SessionInfo, error) { return a.sessions.List() }

// Subscribe calls fn with the events of the current conversation, following
// it across Open, and with App events, one at a time in the order they
// occur. fn runs on the goroutine that caused the event, and must not cause
// another (switch the mode, open a conversation) nor wait for the
// conversation.
func (a *App) Subscribe(fn func(Event)) (unsubscribe func()) { return a.events.subscribe(fn) }

// Close closes the conversation and releases process resources.
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

// Cwd is the workspace the process started in.
func (a *App) Cwd() string { return a.cwd }

// Settings returns the resolved settings.
func (a *App) Settings() config.Resolved {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

// defaultModel is the model a new conversation runs on.
func (a *App) defaultModel() storage.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return storage.Model{Provider: a.settings.Provider, Model: a.settings.Model, Effort: a.settings.ReasoningEffort}
}

// rememberModel makes a model the default for new conversations, in the
// user's settings and in the settings the App runs on.
func (a *App) rememberModel(prov, name, effort string) error {
	if err := config.PatchUserSettings(config.Settings{Provider: &prov, Model: &name, ReasoningEffort: &effort}); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.Provider, a.settings.Model, a.settings.ReasoningEffort = prov, name, effort
	return nil
}

// Mode returns the permission mode.
func (a *App) Mode() interact.Mode { return a.permissions.Mode() }

// SetMode switches the permission mode.
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

// filesystemRoots are where tools read and write unasked under settings.
func filesystemRoots(cwd string, settings config.Resolved, ext *extension.Set) permission.FilesystemRoots {
	memoryDir := config.MemoryDir(cwd)
	// What the local plugins hold runs, or decides what does, as they load.
	var plugins []string
	for _, pl := range ext.Plugins {
		if pl.Src.Dir != "" {
			plugins = append(plugins, pl.Src.Dir)
		}
	}
	// The sandbox worktrees are part of the workspace even where the
	// configured roots leave them out.
	worktrees := worktree.Root(cwd)
	return permission.FilesystemRoots{
		ReadRoots:  append(slices.Clone(settings.Permissions.ReadRoots), config.SessionsDir(cwd), worktrees),
		WriteRoots: append(slices.Clone(settings.Permissions.WriteRoots), worktrees),
		// Auto-memory lives outside the workspace; as a harness-managed
		// path it skips the outside-roots prompt.
		InternalReadable: []string{memoryDir},
		InternalWritable: []string{memoryDir},
		Protected:        plugins,
	}
}

// auditor appends permission decisions to the audit log.
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

// checkProviderSetup validates that settings.json configures the active
// provider. The first-run wizard runs before Boot, so anything missing here
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
