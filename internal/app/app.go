// Package app assembles codebot. App holds the project's state (settings,
// models, permissions, extensions, MCP) and the open Conversations, each of
// which holds one session's state. Frontends use only these two types.
package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"

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
	Cwd string
	// Mode is the permission mode a conversation starts in.
	Mode interact.Mode
	// Frontend is what the conversations Open opens use.
	Frontend
	// CacheTTL is passed as litellm.CacheControl.TTL. Use "1h" when a person
	// is in the loop: they pause longer than the default five minutes.
	CacheTTL string
	// NewModel overrides how models are built; nil uses litellm.
	NewModel ModelFactory
	// Trust trusts the project and the plugins it declares for this process,
	// ignoring saved consent.
	Trust bool
	// PluginDirs are local plugins under development, loaded for this
	// process only with everything they run allowed.
	PluginDirs []string
	// Version is codebot's, the release its traces are of.
	Version string
}

// Frontend is what a conversation asks the user and reads files through.
type Frontend struct {
	// Name tags the conversation's traces: tui, print or acp.
	Name string
	UI   interact.UI
	// FS backs read, write and edit; nil means the local filesystem.
	FS agentcoretools.FS
	// Interactive enables ask_user; headless frontends have no one to ask.
	Interactive bool
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
	// configuration read last is the one in effect. starting serializes
	// opening conversations, so no session is opened twice.
	reloading, connecting, starting sync.Mutex

	mu            sync.Mutex
	conversations map[*Conversation]func() // each open one, with its unsubscribe
	current       *Conversation            // the one Open opened last
	offeredMCP    map[string]config.MCPServer
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

		conversations: make(map[*Conversation]func()),
		offeredMCP:    make(map[string]config.MCPServer),
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
		tracer, shutdown, err := telemetry.Setup(settings.Langfuse, a.opts.Version)
		if err != nil {
			return nil, err
		}
		a.newModel = provider.NewModelFactory(a.models, tracer.Observer)
		a.tracer, a.shutdown = tracer, shutdown
	}
	if a.usage, err = skill.NewUsageTracker(filepath.Join(config.UserConfigDir(), "skill-usage.json")); err != nil {
		return nil, fmt.Errorf("skill usage: %w", err)
	}
	if a.permissions, err = newPermissionEngine(cwd, settings, ext.Set); err != nil {
		return nil, err
	}
	a.mcp = mcp.NewManager(func() { go a.refreshMCP() })

	go tools.CleanOldOutputs(config.SessionsDir(cwd))
	go cleanWorktreeOrphans(cwd)
	go sweepPluginCache()
	return a, nil
}

// Start opens session id, or a new session when id is "", alongside the
// conversations already open; Close closes it. cwd is in the project or in
// another checkout of it, such as a linked git worktree: the project keeps
// the settings, trust, approvals and sessions, while the skills, AGENTS.md
// and file checkpoints follow cwd.
func (a *App) Start(id, cwd string, f Frontend) (*Conversation, error) {
	if !a.holds(cwd) {
		return nil, fmt.Errorf("%s is outside the project %s, which this codebot serves", cwd, a.cwd)
	}
	c, err := a.start(id, cwd, f, a.opts.Mode)
	if err != nil {
		return nil, err
	}
	c.start()
	return c, nil
}

// Open is Start for frontends that show one conversation at a time: it
// opens session id where it ran, or a new session in the project, with
// Options' frontend, in the mode of the conversation it replaces, and closes
// that one. On error the current conversation stays open.
func (a *App) Open(id string) (*Conversation, error) {
	mode := a.opts.Mode
	prev := a.Current()
	if prev != nil {
		mode = prev.Mode()
	}
	c, err := a.start(id, "", a.opts.Frontend, mode)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.current = c
	a.mu.Unlock()
	if prev != nil {
		prev.Close()
	}
	c.start()
	a.events.publish(Event{Kind: Opened, Conversation: c})
	return c, nil
}

// start opens the conversation in cwd; "" is where the session ran, or the
// project for a new one.
func (a *App) start(id, cwd string, f Frontend, mode interact.Mode) (*Conversation, error) {
	a.starting.Lock()
	defer a.starting.Unlock()
	var (
		store *storage.Store
		state storage.State
		err   error
	)
	switch {
	case id == "":
		cwd = cmp.Or(cwd, a.cwd)
		store, err = a.sessions.Create(cwd)
	case slices.ContainsFunc(a.open(), func(c *Conversation) bool { return c.ID() == id }):
		return nil, fmt.Errorf("session %s is already open", id)
	default:
		store, state, err = a.sessions.Open(id)
	}
	if err != nil {
		return nil, err
	}
	if cwd == "" {
		cwd = a.cwd
		if ran := store.Header().Cwd; isDir(ran) && a.holds(ran) {
			cwd = ran
		}
	}
	c, err := openConversation(a, store, state, cwd, f, mode)
	if err != nil {
		store.Close()
		return nil, err
	}
	unsubscribe := c.session.Subscribe(func(ev session.Event) { a.events.publish(Event{Kind: SessionEvent, Session: ev, Conversation: c}) })
	a.mu.Lock()
	a.conversations[c] = unsubscribe
	a.mu.Unlock()
	return c, nil
}

// holds reports whether dir is in the project or in another checkout of it.
func (a *App) holds(dir string) bool { return a.within(dir) || worktree.SamePlace(a.cwd, dir) }

// within reports whether dir is in the project's directory.
func (a *App) within(dir string) bool {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	rel, err := filepath.Rel(real(a.cwd), real(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// open returns the open conversations.
func (a *App) open() []*Conversation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Collect(maps.Keys(a.conversations))
}

// Current is the conversation Open opened last, while it is open.
func (a *App) Current() *Conversation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

type SessionInfo = storage.SessionInfo

// Sessions returns the workspace's sessions, newest first.
func (a *App) Sessions() ([]SessionInfo, error) { return a.sessions.List() }

// Subscribe delivers App events and the events of every open conversation,
// one at a time in order. fn runs on the goroutine
// that caused the event. It must not cause another event (switch the mode,
// open a conversation) or wait on the conversation.
func (a *App) Subscribe(fn func(Event)) (unsubscribe func()) { return a.events.subscribe(fn) }

func (a *App) Close() {
	for _, c := range a.open() {
		c.Close()
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

// Approval is a kind of call the user asked not to be asked about again in
// this project.
type Approval = permission.Remembered

// Approvals returns the project's remembered approvals, newest first.
func (a *App) Approvals() []Approval { return a.permissions.Remembered() }

// Forget drops a remembered approval: its calls are asked about again.
func (a *App) Forget(key string) error { return a.permissions.Forget(key) }

func newPermissionEngine(cwd string, settings config.Resolved, ext *extension.Set) (*permission.Engine, error) {
	rules, err := permission.ParseRuleSet(settings.Permissions.Allow, settings.Permissions.Deny)
	if err != nil {
		return nil, fmt.Errorf("parse permission rules: %w", err)
	}
	engine, err := permission.NewEngine(permission.Config{
		Cwd:     cwd,
		Rules:   rules,
		Roots:   filesystemRoots(cwd, settings, ext),
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
// is an error. Whether the provider needs a key it has not got, building
// the model tells: Ollama needs none.
func checkProviderSetup(settings config.Resolved) error {
	if settings.Provider == "" {
		return errors.New("configuration error: provider is not set in settings.json")
	}
	if settings.Model == "" {
		return errors.New("configuration error: model is not set in settings.json")
	}
	return nil
}
