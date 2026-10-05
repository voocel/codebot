package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/prompt"
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/snapshot"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/worktree"
)

// Conversation is one session and everything that lives as long as it: its
// tools, working directory, background tasks, checkpoints and hooks. Opening
// another session replaces the whole Conversation; nothing is reset.
type Conversation struct {
	app       *App
	id        string
	dir       string // per-session directory: background output, tool output
	session   *session.Session
	tasks     *task.Runtime
	agents    *AgentHub         // background sub-agent runs
	snapshots *snapshot.Tracker // nil when checkpoints are off
	hooks     *hooks.Runner     // nil without hooks
	files     *agentcoretools.FileReadState
	limiter   *tools.OutputLimiter
	validator *validation // nil without hooks

	system []litellm.Block // the system prompt; see context.go

	mu        sync.Mutex
	cwd       string
	worktree  *worktreeState
	model     modelChoice
	workspace []prompt.Part
	tools     []agentcore.Tool // built for the current model
	subagents agentcore.Tool   // the subagent tool among them, for forked skills
	mcpTools  []agentcore.Tool // every MCP tool the conversation had, see growTools
	// grants are the tools the skills invoked in the current run allow; they
	// go when it ends.
	grants []approval.Rule
}

func openConversation(a *App, store *storage.Store, state storage.State) (*Conversation, error) {
	id := store.Header().SessionID
	recorded := state.Model
	if recorded.Model == "" {
		recorded = storage.Model{Provider: a.settings.Provider, Model: a.settings.Model, Effort: a.settings.ReasoningEffort}
	}
	model, err := a.chooseModel(recorded.Provider, recorded.Model, recorded.Effort)
	if err != nil {
		return nil, err
	}

	c := &Conversation{
		app:    a,
		id:     id,
		dir:    filepath.Join(config.SessionsDir(a.cwd), id),
		agents: NewAgentHub(),
		files:  agentcoretools.NewFileReadState(),
		system: a.systemPrompt(),
		cwd:    a.cwd,
		model:  model,
	}
	c.tasks = task.NewRuntime(filepath.Join(c.dir, "tasks"), c.background)
	c.limiter = tools.NewOutputLimiter(filepath.Join(c.dir, tools.ToolOutputsSubdir))
	if a.settings.Snapshot && worktree.IsRepo(a.cwd) {
		c.snapshots = snapshot.New(config.SnapshotDir(a.cwd), a.cwd, config.UndoStatePath(a.cwd, id))
	}
	if c.hooks = hooks.New(a.settings.Hooks, id, a.approval, c.hookModel); c.hooks != nil {
		c.validator = &validation{hooks: c.hooks}
	}
	config.EnsureMemoryDir(a.cwd)
	c.workspace = a.workspace(a.cwd)
	c.tools = c.buildTools()
	c.mcpTools, _ = a.mcpSnapshot()

	if c.session, err = session.Open(store, state, c.specLocked()); err != nil {
		c.tasks.StopAll()
		return nil, err
	}
	a.tracer.SetSession(id)
	if c.hooks != nil {
		c.hooks.RunSessionStart()
		c.session.Subscribe(func(ev session.Event) {
			if ev.Kind == session.Idle {
				c.hooks.RunNotification("agent response complete")
			}
		})
	}
	return c, nil
}

// close ends the conversation: the run, background tasks, a worktree with no
// changes, and the hooks' session.
func (c *Conversation) close() {
	c.session.Close()
	c.tasks.StopAll()
	c.tasks.Wait()
	if c.Worktree() != "" {
		_, _ = c.ExitWorktree(false)
	}
	if c.snapshots != nil {
		c.snapshots.Close()
	}
	if c.hooks != nil {
		c.hooks.RunSessionEnd()
	}
}

// ID is the session ID.
func (c *Conversation) ID() string { return c.id }

// Cwd is the directory the agent works in: the workspace, or the worktree it
// entered.
func (c *Conversation) Cwd() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cwd
}

// Subscribe calls fn with the conversation's session events; see
// session.Session.Subscribe.
func (c *Conversation) Subscribe(fn func(session.Event)) (unsubscribe func()) {
	return c.session.Subscribe(fn)
}

// Submit sends the user's input. UserPromptSubmit hooks run first: a blocking
// hook rejects the input, and context they add goes ahead of it.
func (c *Conversation) Submit(ctx context.Context, blocks []litellm.Block) error {
	msgs, err := c.promptSubmit(ctx, blocks)
	if err != nil {
		return err
	}
	c.post(msgs)
	return nil
}

// promptSubmit runs the UserPromptSubmit hooks over the user's input and
// returns what to post: the context they add, then the input.
func (c *Conversation) promptSubmit(ctx context.Context, blocks []litellm.Block) ([]agentcore.Message, error) {
	input := agentcore.User(blocks...)
	if c.hooks == nil {
		return []agentcore.Message{input}, nil
	}
	dec, err := c.hooks.RunUserPromptSubmit(ctx, input.Text())
	if err != nil {
		return nil, err
	}
	if extra := strings.TrimSpace(dec.AdditionalContext); extra != "" {
		return []agentcore.Message{reminderMessage(extra), input}, nil
	}
	return []agentcore.Message{input}, nil
}

func (c *Conversation) post(msgs []agentcore.Message) {
	for _, m := range msgs {
		c.session.Post(session.Input{Source: session.User, Msg: m})
	}
}

// reminder wraps harness-provided context so the model does not take it for
// the user's words.
func reminder(text string) string {
	return "<system-reminder>\n" + text + "\n</system-reminder>"
}

// reminderMessage is a message of harness-provided context, which frontends
// do not show as the user's.
func reminderMessage(text string) agentcore.Message {
	m := agentcore.UserText(reminder(text))
	m.Kind = kindReminder
	return m
}

// background posts a finished background task's notification.
func (c *Conversation) background(msg agentcore.Message) {
	c.session.Post(session.Input{Source: session.Background, Msg: msg})
}

// Wait returns once the conversation is idle with every earlier input
// handled and every subscriber caught up; see session.Session.Wait.
func (c *Conversation) Wait(ctx context.Context) error { return c.session.Wait(ctx) }

// Cancel stops the current run.
func (c *Conversation) Cancel() { c.session.Cancel() }

// Compact summarizes the history now.
func (c *Conversation) Compact(ctx context.Context) error { return c.session.Compact(ctx) }

// Query answers a side question from the conversation's context without
// adding to it. It thinks as the conversation does: a request that thinks
// otherwise reads none of the conversation from the prompt cache.
func (c *Conversation) Query(ctx context.Context, question string) (string, error) {
	return c.session.Query(ctx, question, 0)
}

// Suggest predicts what the user may type next, or "" when nothing fits. It
// thinks as the conversation does, as Query.
func (c *Conversation) Suggest(ctx context.Context) (string, error) {
	text, err := c.session.Query(ctx, prompt.Suggestion, suggestionMaxTokens)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(strings.Trim(text, "\"'`"))
	if text == "" || strings.EqualFold(text, "NONE") || len(strings.Fields(text)) > 12 {
		return "", nil
	}
	return text, nil
}

// History returns the conversation's history.
func (c *Conversation) History() []agentcore.Message { return c.session.History() }

// Status describes the conversation for display.
type Status struct {
	session.Status
	Mode     interact.Mode
	Cwd      string
	Worktree string // sandbox directory; "" outside a worktree
	Tasks    int    // running background tasks
	// SmallModel runs the explore sub-agent.
	SmallModel string
}

// Status returns the conversation's status.
func (c *Conversation) Status() Status {
	c.mu.Lock()
	small := c.model.small
	c.mu.Unlock()
	return Status{
		Status:     c.session.Status(),
		Mode:       c.app.Mode(),
		Cwd:        c.Cwd(),
		Worktree:   c.Worktree(),
		Tasks:      c.tasks.Active(),
		SmallModel: small,
	}
}

// GitBranch is the branch checked out where the conversation works, ""
// outside a repository.
func (c *Conversation) GitBranch() string { return worktree.CurrentBranch(c.Cwd()) }

// Skills returns the skills active in the conversation's workspace.
func (c *Conversation) Skills() []Skill { return c.app.skillCatalog().List(c.Cwd()) }

// Tasks returns the background task runtime.
func (c *Conversation) Tasks() *task.Runtime { return c.tasks }

// Agents returns the hub of background sub-agent runs.
func (c *Conversation) Agents() *AgentHub { return c.agents }

// SetModel switches to the model name served by the provider configured as
// prov, with the reasoning effort ("" is the provider default), and
// remembers the choice in the settings.
func (c *Conversation) SetModel(prov, name, effort string) error {
	choice, err := c.app.chooseModel(prov, name, effort)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.model = choice
	c.tools = c.buildTools()
	c.configureLocked()
	c.mu.Unlock()
	return config.PatchEffectiveSettings(c.app.cwd, config.Settings{Provider: &prov, Model: &name, ReasoningEffort: &effort})
}

// hookModel is the model prompt hooks call: the conversation's, without its
// reasoning effort.
func (c *Conversation) hookModel() agentcore.Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model.model
}

// suggestionMaxTokens bounds a suggestion's response, reasoning included.
const suggestionMaxTokens = 2048

// mcpChanged adds the tools the MCP servers now offer to the conversation's,
// which only grow (see growTools); they apply from the next run.
func (c *Conversation) mcpChanged(tools []agentcore.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mcpTools = growTools(c.mcpTools, tools)
	c.configureLocked()
}

// Reload re-reads the workspace the model is told about, after the user
// reloaded plugins. The model is told what changed as the next run starts.
func (c *Conversation) Reload() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workspace = c.app.workspace(c.cwd)
	c.configureLocked()
}

// InvokeSkill runs a skill the user invoked as a command. An inline skill is
// submitted as the user's input; a forked one runs in a sub-agent whose output
// is returned.
func (c *Conversation) InvokeSkill(ctx context.Context, name, args string) (string, error) {
	inv, err := c.app.skillCatalog().Invoke(ctx, skill.InvokeInput{
		Name:      name,
		Args:      args,
		Cwd:       c.Cwd(),
		SessionID: c.id,
		By:        skill.ByUser,
	})
	if err != nil {
		return "", err
	}
	if inv.Fork {
		c.skillInvoked(inv)
		res, err := tools.ForkSkill(ctx, inv, c.forkSkill)
		if err != nil {
			return "", err
		}
		return res.Text(), nil
	}
	msgs, err := c.promptSubmit(ctx, []litellm.Block{litellm.Text(inv.Prompt)})
	if err != nil {
		return "", err
	}
	// Granted before the prompt is posted, so the run it lands in has the
	// skill's tools.
	c.skillInvoked(inv)
	c.post(msgs)
	return "", nil
}

// skillInvoked records a skill's use. An inline skill's allowed tools are
// granted until the current run ends.
func (c *Conversation) skillInvoked(inv *skill.Invocation) {
	_ = c.app.usage.Record(inv.Spec.Name, time.Now())
	if inv.Fork {
		return
	}
	grants := approval.ParseGrants(inv.AllowedTools)
	c.mu.Lock()
	c.grants = append(c.grants, grants...)
	c.mu.Unlock()
}

// skillGrants returns what the skills of the current run allow.
func (c *Conversation) skillGrants() []approval.Rule {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.grants
}

// errNoSnapshots explains Undo, Redo and Diff without checkpoints.
var errNoSnapshots = errors.New("file checkpoints are off: they need a git repository and the snapshot setting")

// Undo reverts the files changed by the most recent run that changed any; ok
// is false when there is nothing to undo.
func (c *Conversation) Undo() (changed []string, ok bool, err error) {
	if c.snapshots == nil {
		return nil, false, errNoSnapshots
	}
	return c.snapshots.Undo()
}

// Redo re-applies the most recent Undo.
func (c *Conversation) Redo() (changed []string, ok bool, err error) {
	if c.snapshots == nil {
		return nil, false, errNoSnapshots
	}
	return c.snapshots.Redo()
}

// Diff previews what Undo would revert, "" when nothing.
func (c *Conversation) Diff() (string, error) {
	if c.snapshots == nil {
		return "", errNoSnapshots
	}
	return c.snapshots.DiffTop()
}
