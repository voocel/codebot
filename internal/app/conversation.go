package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/agent/prompt"
	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/agent/tools"
	"github.com/voocel/codebot/internal/extension/hooks"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/internal/session/storage"
	"github.com/voocel/codebot/internal/workspace/snapshot"
	"github.com/voocel/codebot/internal/workspace/worktree"
)

// Conversation holds everything that lives as long as one session; another
// session is another Conversation, so nothing is ever reset.
type Conversation struct {
	app        *App
	id         string
	dir        string
	frontend   Frontend
	home       string // the checkout it opened in, which it returns to from a worktree
	session    *session.Session
	tasks      *task.Runtime
	agents     *AgentHub
	snapshots  *snapshot.Tracker // nil when checkpoints are off
	hooks      *hooks.Runner
	files      *agentcoretools.FileReadState
	limiter    *tools.OutputLimiter
	validation *hooks.Validation
	modeMu     sync.Mutex // orders mode changes with their events

	system []litellm.Block // see context.go

	// mu guards the fields below. Never take the App's lock while holding it.
	mu        sync.Mutex
	mode      interact.Mode
	cwd       string
	worktree  *worktreeState
	model     modelChoice
	workspace []prompt.Part
	skills    *skill.Catalog
	tools     []agentcore.Tool
	subagents agentcore.Tool   // the subagent tool in tools, used by forked skills
	mcpTools  []agentcore.Tool // every MCP tool seen so far; see growTools
	// grants are tools allowed by skills invoked in the current run. They are
	// dropped when the run ends.
	grants []permission.Rule
}

func openConversation(a *App, store *storage.Store, state storage.State, cwd string, f Frontend, mode interact.Mode) (*Conversation, error) {
	id := store.Header().SessionID
	recorded := state.Model
	if recorded.Model == "" {
		recorded = a.defaultModel()
	}
	model, err := a.chooseModel(recorded.Provider, recorded.Model, recorded.Effort)
	if err != nil {
		return nil, err
	}

	c := &Conversation{
		app:      a,
		id:       id,
		dir:      filepath.Join(config.SessionsDir(a.cwd), id),
		frontend: f,
		home:     cwd,
		agents:   NewAgentHub(),
		files:    agentcoretools.NewFileReadState(),
		system:   a.systemPrompt(),
		mode:     mode,
		cwd:      cwd,
		model:    model,
	}
	c.tasks = task.NewRuntime(filepath.Join(c.dir, "tasks"), c.background)
	c.limiter = tools.NewOutputLimiter(filepath.Join(c.dir, tools.ToolOutputsSubdir))
	if a.settings.Snapshot && worktree.IsRepo(cwd) {
		c.snapshots = snapshot.New(config.SnapshotDir(cwd), cwd)
	}
	c.hooks = hooks.New(id, c.hookModel, c.Cwd)
	c.hooks.Set(a.Extensions().HooksConfig())
	c.validation = hooks.NewValidation(c.hooks)
	config.EnsureMemoryDir(a.cwd)
	c.skills, c.workspace = a.workspace(cwd)
	c.tools = c.buildTools()
	c.mcpTools = a.offered.Load().tools

	if c.session, err = session.Open(store, state, c.specLocked()); err != nil {
		c.tasks.StopAll()
		return nil, err
	}
	return c, nil
}

// start runs once the conversation is open, and with Open once the one it
// replaces has closed. It re-reads the MCP offer in case a refresh came
// before the conversation was open.
func (c *Conversation) start() {
	c.mcpChanged()
	c.hooks.RunSessionStart()
	c.session.Subscribe(func(ev session.Event) {
		if ev.Kind == session.Idle {
			c.hooks.RunNotification("agent response complete")
		}
	})
}

// Close ends the conversation; its session stays to resume.
func (c *Conversation) Close() {
	a := c.app
	a.mu.Lock()
	unsubscribe, open := a.conversations[c]
	delete(a.conversations, c)
	if a.current == c {
		a.current = nil
	}
	a.mu.Unlock()
	if open {
		unsubscribe()
		c.close()
	}
}

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
	c.hooks.RunSessionEnd()
}

func (c *Conversation) ID() string { return c.id }

// Cwd is the workspace, or the worktree the agent entered.
func (c *Conversation) Cwd() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cwd
}

func (c *Conversation) Subscribe(fn func(session.Event)) (unsubscribe func()) {
	return c.session.Subscribe(fn)
}

// Submit runs UserPromptSubmit hooks first: a blocking hook rejects the
// input, and context they add is posted before it.
func (c *Conversation) Submit(ctx context.Context, blocks []litellm.Block) error {
	msgs, err := c.promptSubmit(ctx, blocks)
	if err != nil {
		return err
	}
	c.post(msgs)
	return nil
}

// Prompt is Submit for frontends without commands of their own: input that
// names a skill the user can invoke, "/name args", invokes it, and the answer
// of a skill that forks is returned.
func (c *Conversation) Prompt(ctx context.Context, blocks []litellm.Block) (string, error) {
	if name, args, ok := c.skillCommand(blocks); ok {
		return c.InvokeSkill(ctx, name, args)
	}
	return "", c.Submit(ctx, blocks)
}

// skillCommand reads text alone: a skill takes no images.
func (c *Conversation) skillCommand(blocks []litellm.Block) (name, args string, ok bool) {
	var text strings.Builder
	for _, b := range blocks {
		t, isText := b.(litellm.TextBlock)
		if !isText {
			return "", "", false
		}
		text.WriteString(t.Text)
	}
	line, ok := strings.CutPrefix(strings.TrimSpace(text.String()), "/")
	if !ok {
		return "", "", false
	}
	name, args, _ = strings.Cut(line, " ")
	ok = slices.ContainsFunc(c.SkillCommands(), func(s Skill) bool { return s.Name == name })
	return name, strings.TrimSpace(args), ok
}

func (c *Conversation) promptSubmit(ctx context.Context, blocks []litellm.Block) ([]agentcore.Message, error) {
	input := agentcore.User(blocks...)
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

// reminder marks harness context so the model doesn't mistake it for the
// user's words.
func reminder(text string) string {
	return "<system-reminder>\n" + text + "\n</system-reminder>"
}

// kindReminder marks messages the harness adds, such as validation failures
// or hook context. Frontends don't show them as the user's.
const kindReminder = "reminder"

func reminderMessage(text string) agentcore.Message {
	m := agentcore.UserText(reminder(text))
	m.Kind = kindReminder
	return m
}

func (c *Conversation) background(msg agentcore.Message) {
	c.session.Post(session.Input{Source: session.Background, Msg: msg})
}

func (c *Conversation) Wait(ctx context.Context) error { return c.session.Wait(ctx) }

func (c *Conversation) Cancel() { c.session.Cancel() }

func (c *Conversation) Compact(ctx context.Context) error {
	ctx, work := c.app.tracer.Work(c.runContext(ctx), "compact", "")
	err := c.session.Compact(ctx)
	work.End("", err)
	return err
}

// Query answers a side question without adding to the history. It keeps the
// conversation's reasoning settings, because a request with different ones
// misses the prompt cache.
func (c *Conversation) Query(ctx context.Context, question string) (string, error) {
	ctx, work := c.app.tracer.Work(c.runContext(ctx), "btw", question)
	answer, err := c.session.Query(ctx, question, 0)
	work.End(answer, err)
	return answer, err
}

// Suggest predicts the user's next input, or "" when nothing fits. Like
// Query, it keeps the conversation's reasoning settings.
func (c *Conversation) Suggest(ctx context.Context) (string, error) {
	ctx, work := c.app.tracer.Work(c.runContext(ctx), "suggestion", "")
	text, err := c.session.Query(ctx, prompt.Suggestion, suggestionMaxTokens)
	if err == nil {
		text = strings.TrimSpace(strings.Trim(text, "\"'`"))
		if strings.EqualFold(text, "NONE") || len(strings.Fields(text)) > 12 {
			text = ""
		}
	}
	work.End(text, err)
	return text, err
}

func (c *Conversation) History() []agentcore.Message { return c.session.History() }

type Status struct {
	session.Status
	Mode     interact.Mode
	Cwd      string
	Worktree string // sandbox directory; "" outside a worktree
	Tasks    int    // running background tasks
	// SmallModel runs the explore sub-agent.
	SmallModel string
	// Reasoning reports whether the model accepts a reasoning effort. An
	// empty Effort means the provider default.
	Reasoning bool
}

func (c *Conversation) Status() Status {
	c.mu.Lock()
	small, reasoning := c.model.small, c.model.reasoning
	c.mu.Unlock()
	return Status{
		Status:     c.session.Status(),
		Mode:       c.Mode(),
		Cwd:        c.Cwd(),
		Worktree:   c.Worktree(),
		Tasks:      c.tasks.Active(),
		SmallModel: small,
		Reasoning:  reasoning,
	}
}

// Model returns the provider, model and reasoning effort of the next run.
func (c *Conversation) Model() (prov, name, effort string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model.provider, c.model.name, c.model.effort
}

func (c *Conversation) Mode() interact.Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// SetMode sets the permission mode of the conversation's tool calls.
func (c *Conversation) SetMode(m interact.Mode) {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	c.mu.Lock()
	changed := c.mode != m
	c.mode = m
	c.mu.Unlock()
	if changed {
		c.app.events.publish(Event{Kind: ModeChanged, Conversation: c, Mode: m})
	}
}

// GitBranch returns "" outside a repository.
func (c *Conversation) GitBranch() string { return worktree.CurrentBranch(c.Cwd()) }

func (c *Conversation) Skills() []Skill { return c.activeSkills().List() }

// SkillCommands returns the skills the user can invoke as "/name args".
func (c *Conversation) SkillCommands() []Skill {
	return slices.DeleteFunc(c.Skills(), func(s Skill) bool { return s.DisableUserInvocation })
}

func (c *Conversation) activeSkills() *skill.Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skills
}

func (c *Conversation) Tasks() *task.Runtime { return c.tasks }

func (c *Conversation) Agents() *AgentHub { return c.agents }

// SetModel also saves the choice as the default in the user's settings. An
// empty effort means the provider default.
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
	return c.app.rememberModel(prov, name, effort)
}

// hookModel is the conversation's model without its reasoning effort.
func (c *Conversation) hookModel() agentcore.Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model.model
}

// suggestionMaxTokens bounds a suggestion's response, reasoning included.
const suggestionMaxTokens = 2048

// mcpChanged applies the current MCP offer from the next run on. Tools only
// accumulate (see growTools); instructions are replaced. The offer is read
// under c.mu, so among concurrent calls the last one applies the latest.
func (c *Conversation) mcpChanged() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mcpTools = growTools(c.mcpTools, c.app.offered.Load().tools)
	c.configureLocked()
}

// Reload applies the App's reloaded extensions and re-reads the workspace.
// The model learns what changed when the next run starts.
func (c *Conversation) Reload() {
	c.hooks.Set(c.app.Extensions().HooksConfig())
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skills, c.workspace = c.app.workspace(c.cwd)
	c.tools = c.buildTools()
	c.configureLocked()
}

// InvokeSkill submits an inline skill as user input. A forked skill runs in
// a sub-agent and its output is returned.
func (c *Conversation) InvokeSkill(ctx context.Context, name, args string) (string, error) {
	inv, err := c.activeSkills().Invoke(ctx, skill.InvokeInput{
		Name:      name,
		Args:      args,
		SessionID: c.id,
		By:        skill.ByUser,
	})
	if err != nil {
		return "", err
	}
	if inv.Fork {
		c.skillInvoked(inv)
		ctx, work := c.app.tracer.Work(c.runContext(ctx), "skill "+name, strings.TrimSpace("/"+name+" "+args))
		res, err := tools.ForkSkill(ctx, inv, c.forkSkill)
		work.End(res.Text(), err)
		if err != nil {
			return "", err
		}
		return res.Text(), nil
	}
	line := strings.TrimSpace("/" + name + " " + args)
	msgs, err := c.promptSubmit(ctx, []litellm.Block{litellm.Text(line), litellm.Text(inv.Prompt)})
	if err != nil {
		return "", err
	}
	msgs[len(msgs)-1].Kind = kindSkill
	// Grant before posting so the run that takes the prompt has the skill's
	// tools.
	c.skillInvoked(inv)
	c.post(msgs)
	return "", nil
}

// skillInvoked grants an inline skill's allowed tools until the current run
// ends.
func (c *Conversation) skillInvoked(inv *skill.Invocation) {
	_ = c.app.usage.Record(inv.Spec.Name, time.Now())
	if inv.Fork {
		return
	}
	grants := permission.ParseGrants(inv.AllowedTools)
	c.mu.Lock()
	c.grants = append(c.grants, grants...)
	c.mu.Unlock()
}

func (c *Conversation) skillGrants() []permission.Rule {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.grants
}

var errNoSnapshots = errors.New("file checkpoints are off: they need a git repository and the snapshot setting")

// Checkpoint is a point the conversation can go back to: before a run the
// user started.
type Checkpoint struct {
	Prompt string // what the user asked
	Time   time.Time
	cp     storage.Checkpoint
}

// Checkpoints returns the points the conversation can go back to, oldest
// first.
func (c *Conversation) Checkpoints() []Checkpoint {
	cps, history := c.session.Checkpoints()
	var out []Checkpoint
	for i, cp := range cps {
		end := len(history)
		if i+1 < len(cps) {
			end = cps[i+1].At
		}
		// A run a finished background task started has no prompt.
		for _, m := range history[cp.At:end] {
			if text, ok := UserText(m); ok && m.Role == litellm.RoleUser {
				out = append(out, Checkpoint{Prompt: text, Time: m.Time, cp: cp})
				break
			}
		}
	}
	return out
}

// Changed lists the files going back to cp would put back. It fails when
// they can't go back: checkpoints are off, the conversation has moved to
// another workspace since, or the checkpoint expired.
func (c *Conversation) Changed(cp Checkpoint) ([]string, error) {
	if c.snapshots == nil || cp.cp.Tree == "" {
		return nil, errNoSnapshots
	}
	return c.snapshots.Changed(cp.cp.Dir, cp.cp.Tree)
}

// Rewind goes back to before cp's run: the files, the conversation, or both,
// all between runs, and nothing when any of it can't. It reports the files it
// put back. Files put back under a conversation that stays are pointed out to
// the agent, which would otherwise go on from its changes.
func (c *Conversation) Rewind(cp Checkpoint, files, conversation bool) ([]string, error) {
	var changed []string
	err := c.session.Edit(func(e session.Editor) error {
		if conversation && !e.Holds(cp.cp) {
			return errors.New("the conversation no longer holds that request")
		}
		if files {
			if c.snapshots == nil || cp.cp.Tree == "" {
				return errNoSnapshots
			}
			// Restore changes nothing when it fails.
			var err error
			if changed, err = c.snapshots.Restore(cp.cp.Dir, cp.cp.Tree); err != nil {
				return err
			}
		}
		switch {
		case conversation:
			return e.Rewind(cp.cp)
		case len(changed) > 0:
			return e.Append(reminderMessage(fmt.Sprintf("The user put the files back as they were before the request %q: what was changed since is undone (%s). Read them again before relying on them.", cp.Prompt, strings.Join(changed, ", "))))
		}
		return nil
	})
	return changed, err
}
