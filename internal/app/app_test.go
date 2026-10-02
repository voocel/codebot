package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/internal/todo"
)

const timeout = 10 * time.Second

// call is what the model received in one call.
type call struct {
	system []string
	msgs   []agentcore.Message
	tools  []string
}

// fakeModel is a provider answering each call with the next scripted reply,
// then "done".
type fakeModel struct {
	mu      sync.Mutex
	replies []litellmtest.Reply
	calls   []call
}

func script(replies ...litellmtest.Reply) *fakeModel { return &fakeModel{replies: replies} }

func (m *fakeModel) Name() string { return "fake" }

func (m *fakeModel) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	return litellmtest.New(m.next(req)).Chat(ctx, req)
}

func (m *fakeModel) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	return litellmtest.New(m.next(req)).Stream(ctx, req)
}

func (m *fakeModel) next(req *litellm.Request) litellmtest.Reply {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c call
	for _, msg := range req.Messages {
		if msg.Role == litellm.RoleSystem {
			for _, b := range msg.Blocks {
				c.system = append(c.system, b.(litellm.TextBlock).Text)
			}
		} else {
			c.msgs = append(c.msgs, agentcore.Message{Role: msg.Role, Blocks: msg.Blocks})
		}
	}
	for _, t := range req.Tools {
		c.tools = append(c.tools, t.Name)
	}
	m.calls = append(m.calls, c)
	next := text("done")
	if len(m.replies) > 0 {
		next, m.replies = m.replies[0], m.replies[1:]
	}
	return next
}

func (m *fakeModel) call(i int) call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[i]
}

func (m *fakeModel) last() call { return m.call(m.count() - 1) }

func (m *fakeModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func usage() litellm.Usage { return litellm.Usage{InputTokens: 100, OutputTokens: 10} }

func text(s string) litellmtest.Reply {
	r := litellmtest.Text(s)
	r.Usage = usage()
	return r
}

func use(id, tool string, args any) litellmtest.Reply {
	raw, _ := json.Marshal(args)
	r := litellmtest.Respond(litellm.ToolUseBlock{ID: id, Name: tool, Arguments: string(raw)})
	r.Usage = usage()
	return r
}

// fakeUI answers approvals with choice and records them.
type fakeUI struct {
	mu        sync.Mutex
	choice    interact.Choice
	approvals []interact.Approval
}

func (u *fakeUI) Approve(_ context.Context, req interact.Approval) (interact.Choice, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.approvals = append(u.approvals, req)
	return u.choice, nil
}

func (u *fakeUI) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}

func (u *fakeUI) asked() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var tools []string
	for _, a := range u.approvals {
		tools = append(tools, a.Tool)
	}
	return tools
}

type env struct {
	t      *testing.T
	cwd    string
	ui     *fakeUI
	app    *App
	models map[string]*fakeModel // by model name
	idle   chan struct{}
}

type setup struct {
	mode     interact.Mode
	model    string // default claude-sonnet-4-5
	settings map[string]any
	git      bool
}

// boot starts an App in a throwaway home and workspace, its models scripted.
func boot(t *testing.T, s setup, models map[string]*fakeModel) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	if s.git {
		gitInit(t, cwd)
	}
	if s.model == "" {
		s.model = "claude-sonnet-4-5"
	}
	if s.mode == "" {
		s.mode = interact.ModeBalanced
	}
	settings := map[string]any{
		"provider":  "anthropic",
		"model":     s.model,
		"snapshot":  false,
		"providers": map[string]any{"anthropic": map[string]any{"api_key": "test", "models": []string{"claude-sonnet-4-5", "claude-opus-4-5"}}},
	}
	for k, v := range s.settings {
		settings[k] = v
	}
	data, _ := json.Marshal(settings)
	if err := os.MkdirAll(filepath.Join(home, ".codebot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codebot", "settings.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	e := &env{t: t, cwd: cwd, ui: &fakeUI{choice: interact.AllowOnce}, models: models, idle: make(chan struct{}, 64)}
	a, err := Boot(Options{
		Cwd:         cwd,
		Mode:        s.mode,
		UI:          e.ui,
		Interactive: true,
		NewModel: func(spec provider.ModelSpec) (agentcore.Model, error) {
			var p litellm.Provider = script()
			if m, ok := models[spec.Model]; ok {
				p = m
			}
			client, err := litellm.New(p)
			if err != nil {
				return agentcore.Model{}, err
			}
			return agentcore.Model{Client: client, Request: litellm.Request{Model: spec.Model}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.Subscribe(func(ev Event) {
		if ev.Kind == SessionEvent && ev.Session.Kind == session.Idle {
			e.idle <- struct{}{}
		}
	})
	e.app = a
	return e
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// submit sends text and waits for the conversation to go idle.
func (e *env) submit(text string) {
	e.t.Helper()
	c := e.app.Current()
	if err := c.Submit(context.Background(), []litellm.Block{litellm.Text(text)}); err != nil {
		e.t.Fatal(err)
	}
	select {
	case <-e.idle:
	case <-time.After(timeout):
		e.t.Fatal("conversation did not go idle")
	}
}

// texts renders the history: "user:hi", "assistant:done", "call:write",
// "tool:w1" ("tool:w1!" for an error).
func texts(msgs []agentcore.Message) []string {
	var out []string
	for _, m := range msgs {
		if result, ok := m.ToolResult(); ok {
			id := result.ToolUseID
			if result.IsError {
				id += "!"
			}
			out = append(out, "tool:"+id)
			continue
		}
		switch {
		case m.Kind == agentcore.KindSummary:
			out = append(out, "summary")
		case len(m.ToolCalls()) > 0:
			out = append(out, "call:"+m.ToolCalls()[0].Name)
		default:
			out = append(out, string(m.Role)+":"+m.Text())
		}
	}
	return out
}

func TestSubmitRunsToIdle(t *testing.T) {
	model := script(text("hello"))
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})

	e.submit("hi")

	if got, want := texts(e.app.Current().History()), []string{"user:hi", "assistant:hello"}; !slices.Equal(got, want) {
		t.Fatalf("history = %q, want %q", got, want)
	}
	system := model.last().system
	if len(system) == 0 || !strings.Contains(system[0], "Working directory: "+e.cwd) {
		t.Fatalf("identity block does not state the workspace: %q", system)
	}
}

func TestOpenResumesASession(t *testing.T) {
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script(text("hello"))})
	e.submit("hi")
	first := e.app.Current()
	id := first.ID()

	fresh, err := e.app.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID() == id || len(fresh.History()) != 0 {
		t.Fatalf("Open(\"\") did not start a new session")
	}
	resumed, err := e.app.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(resumed.History()); !slices.Equal(got, []string{"user:hi", "assistant:hello"}) {
		t.Fatalf("resumed history = %q", got)
	}
	// The replaced conversation is closed: its input goes nowhere.
	_ = first.Submit(context.Background(), []litellm.Block{litellm.Text("late")})
	if n := len(resumed.History()); n != 2 {
		t.Fatalf("closed conversation reached the open one: %d messages", n)
	}
}

func TestApprovalAsksTheUI(t *testing.T) {
	model := script(use("t1", "bash", map[string]string{"command": "touch made"}), text("ok"))
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	e.ui.choice = interact.Deny

	e.submit("make a file")

	if got := e.ui.asked(); !slices.Equal(got, []string{"bash"}) {
		t.Fatalf("approvals = %q, want [bash]", got)
	}
	if _, err := os.Stat(filepath.Join(e.cwd, "made")); !os.IsNotExist(err) {
		t.Fatalf("denied command ran: %v", err)
	}
	if got := texts(e.app.Current().History()); !slices.Contains(got, "tool:t1!") {
		t.Fatalf("history = %q, want a failed bash result", got)
	}
}

// A sub-agent runs its own loop under the same permission mode.
func TestSubagentToolCallsAskTheUI(t *testing.T) {
	model := script(
		use("s1", "subagent", map[string]string{"agent": "general-purpose", "task": "make a file"}),
		use("t1", "bash", map[string]string{"command": "touch made"}),
		text("could not"),
		text("ok"),
	)
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	e.ui.choice = interact.Deny

	e.submit("delegate it")

	if got := e.ui.asked(); !slices.Equal(got, []string{"bash"}) {
		t.Fatalf("approvals = %q, want [bash]", got)
	}
	if _, err := os.Stat(filepath.Join(e.cwd, "made")); !os.IsNotExist(err) {
		t.Fatalf("denied command ran: %v", err)
	}
}

// The tools a skill allows run unasked for the rest of the run that invoked
// it, and are asked about again in the next one.
func TestSkillGrantsLastForTheRun(t *testing.T) {
	model := script(
		use("k1", "skill", map[string]string{"skill": "toucher"}),
		use("b1", "bash", map[string]string{"command": "touch made"}),
		text("ok"),
		use("b2", "bash", map[string]string{"command": "touch again"}),
		text("ok"),
	)
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	e.ui.choice = interact.Deny
	root := filepath.Join(os.Getenv("HOME"), ".codebot", "plugins", "kit")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"plugin.json":       `{"id":"kit","name":"kit","version":"0.1.0","skillsDir":"./skills"}`,
		"skills/toucher.md": "---\ndescription: touches files\nallowed-tools: [\"Bash(touch *)\"]\n---\nTouch the file.\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.app.ReloadPlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.SetPluginTrusted(context.Background(), "kit", true); err != nil {
		t.Fatal(err)
	}

	e.submit("touch it")
	if _, err := os.Stat(filepath.Join(e.cwd, "made")); err != nil {
		t.Fatalf("the granted command did not run: %v", err)
	}
	if got := e.ui.asked(); len(got) != 0 {
		t.Fatalf("approvals during the skill's run = %q, want none", got)
	}

	e.submit("again")
	if got := e.ui.asked(); !slices.Equal(got, []string{"bash"}) {
		t.Fatalf("approvals in the next run = %q, want [bash]", got)
	}
	if _, err := os.Stat(filepath.Join(e.cwd, "again")); !os.IsNotExist(err) {
		t.Fatalf("the grant outlived its run: %v", err)
	}
}

func TestDeferredToolsLoadThroughToolSearch(t *testing.T) {
	model := script()
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})

	e.submit("hi")

	c := model.last()
	if !slices.Contains(c.tools, "tool_search") || slices.Contains(c.tools, "web_fetch") {
		t.Fatalf("tools sent = %q, want tool_search and no web_fetch", c.tools)
	}
}

func TestOtherModelsGetEveryTool(t *testing.T) {
	model := script()
	e := boot(t, setup{model: "claude-haiku-4-5"}, map[string]*fakeModel{"claude-haiku-4-5": model})

	e.submit("hi")

	c := model.last()
	if slices.Contains(c.tools, "tool_search") || !slices.Contains(c.tools, "web_fetch") {
		t.Fatalf("tools sent = %q", c.tools)
	}
}

func TestTodosComeFromTheHistory(t *testing.T) {
	todos := map[string]any{"todos": []map[string]string{{"content": "write tests", "status": "in_progress"}}}
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script(use("t1", "todo_write", todos), text("ok"))})

	e.submit("plan")

	got := todo.FromHistory(e.app.Current().History())
	if len(got) != 1 || got[0].Content != "write tests" {
		t.Fatalf("todos = %+v", got)
	}
}

func TestSetModelAppliesToTheNextRun(t *testing.T) {
	sonnet, opus := script(), script(text("from opus"))
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": sonnet, "claude-opus-4-5": opus})
	c := e.app.Current()

	if err := c.SetModel("anthropic", "claude-opus-4-5", ""); err != nil {
		t.Fatal(err)
	}
	e.submit("hi")

	if sonnet.count() != 0 || opus.count() != 1 {
		t.Fatalf("calls: sonnet %d, opus %d", sonnet.count(), opus.count())
	}
	if st := c.Status(); st.Model != "claude-opus-4-5" {
		t.Fatalf("status model = %q", st.Model)
	}
	settings, err := config.ResolveAllStrict(e.cwd)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "claude-opus-4-5" {
		t.Fatalf("settings model = %q, want the new model remembered", settings.Model)
	}

	// The session records the model it ran on; resuming picks it up.
	resumed, err := e.app.Open(c.ID())
	if err != nil {
		t.Fatal(err)
	}
	if st := resumed.Status(); st.Model != "claude-opus-4-5" {
		t.Fatalf("resumed model = %q", st.Model)
	}
}

func TestPostStopValidationSendsTheAgentBackOnce(t *testing.T) {
	model := script(
		use("w1", "write", map[string]string{"file_path": "a.txt", "content": "x"}),
		text("done"),
		text("still done"),
	)
	hooks := map[string]any{"PostStopValidation": []map[string]any{{"type": "command", "command": "echo broken; exit 1"}}}
	e := boot(t, setup{mode: interact.ModeTrust, settings: map[string]any{"hooks": hooks}}, map[string]*fakeModel{"claude-sonnet-4-5": model})

	e.submit("write it")

	got := texts(e.app.Current().History())
	var reminders int
	for _, s := range got {
		if strings.HasPrefix(s, "user:<system-reminder>") && strings.Contains(s, "broken") {
			reminders++
		}
	}
	if reminders != 1 || got[len(got)-1] != "assistant:still done" {
		t.Fatalf("history = %q, want one validation reminder and the run to end", got)
	}
}

// A sub-agent's edits count too: they run the PostToolUse hooks and the
// validation that follows them.
func TestSubagentEditsAreValidated(t *testing.T) {
	model := script(
		use("s1", "subagent", map[string]string{"agent": "general-purpose", "task": "write a.txt"}),
		use("w1", "write", map[string]string{"file_path": "a.txt", "content": "x"}),
		text("written"),
		text("done"),
		text("still done"),
	)
	posted := filepath.Join(t.TempDir(), "posted")
	hooks := map[string]any{
		"PostToolUse":        []map[string]any{{"type": "command", "matcher": "write", "command": "touch " + posted}},
		"PostStopValidation": []map[string]any{{"type": "command", "command": "echo broken; exit 1"}},
	}
	e := boot(t, setup{mode: interact.ModeTrust, settings: map[string]any{"hooks": hooks}}, map[string]*fakeModel{"claude-sonnet-4-5": model})

	e.submit("delegate it")

	var reminders int
	for _, s := range texts(e.app.Current().History()) {
		if strings.HasPrefix(s, "user:<system-reminder>") && strings.Contains(s, "broken") {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("validation reminders = %d, want 1", reminders)
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(posted); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the PostToolUse hook did not run for the sub-agent's write")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNotificationHookFiresWhenTheAgentIsDone(t *testing.T) {
	out := filepath.Join(t.TempDir(), "notified")
	hooks := map[string]any{"Notification": []map[string]any{{"type": "command", "command": "cat > " + out}}}
	e := boot(t, setup{settings: map[string]any{"hooks": hooks}}, map[string]*fakeModel{"claude-sonnet-4-5": script(text("hello"))})

	e.submit("hi")

	// The hook runs asynchronously.
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(out)
		if strings.Contains(string(data), "agent response complete") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Notification hook payload = %q", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWorktreeMovesTheConversation(t *testing.T) {
	model := script()
	e := boot(t, setup{git: true}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	c := e.app.Current()

	dir, err := c.EnterWorktree("try")
	if err != nil {
		t.Fatal(err)
	}
	if c.Cwd() != dir || c.Worktree() != dir {
		t.Fatalf("cwd = %q, worktree = %q, want %q", c.Cwd(), c.Worktree(), dir)
	}
	e.submit("hi")
	if system := model.last().system; !strings.Contains(system[0], "Working directory: "+dir) {
		t.Fatalf("identity block does not state the worktree: %q", system[0])
	}

	res, err := c.ExitWorktree(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept || c.Cwd() != e.cwd || c.Worktree() != "" {
		t.Fatalf("exit = %+v, cwd = %q", res, c.Cwd())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("clean worktree was not removed: %v", err)
	}
}

// A skill gated on paths follows the conversation into a worktree.
func TestWorktreeSkillsFollowTheWorkspace(t *testing.T) {
	e := boot(t, setup{git: true}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	root := filepath.Join(os.Getenv("HOME"), ".codebot", "plugins", "gated")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"plugin.json":      `{"id":"gated","name":"gated","version":"0.1.0","skillsDir":"./skills"}`,
		"skills/marked.md": "---\ndescription: only where marker.txt is\npaths: [marker.txt]\n---\nbody\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.app.ReloadPlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := e.app.Current()
	active := func() bool {
		return slices.ContainsFunc(c.Skills(), func(s Skill) bool { return s.Name == "marked" })
	}
	if active() {
		t.Fatal("skill is active without its marker")
	}

	dir, err := c.EnterWorktree("try")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !active() {
		t.Fatal("skill is not active in the worktree holding its marker")
	}
}

func TestUndoRevertsTheLastRun(t *testing.T) {
	model := script(use("w1", "write", map[string]string{"file_path": "a.txt", "content": "x"}), text("ok"))
	e := boot(t, setup{git: true, mode: interact.ModeTrust, settings: map[string]any{"snapshot": true}}, map[string]*fakeModel{"claude-sonnet-4-5": model})

	e.submit("write it")
	if _, err := os.Stat(filepath.Join(e.cwd, "a.txt")); err != nil {
		t.Fatal(err)
	}
	changed, ok, err := e.app.Current().Undo()
	if err != nil || !ok {
		t.Fatalf("undo: ok=%v err=%v", ok, err)
	}
	if !slices.Contains(changed, "a.txt") {
		t.Fatalf("changed = %q", changed)
	}
	if _, err := os.Stat(filepath.Join(e.cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("file survived undo: %v", err)
	}
}

func TestQueryLeavesTheHistoryAlone(t *testing.T) {
	model := script(text("hello"), text("an answer"))
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	e.submit("hi")

	answer, err := e.app.Current().Query(context.Background(), "what?")
	if err != nil || answer != "an answer" {
		t.Fatalf("query = %q, %v", answer, err)
	}
	if n := len(e.app.Current().History()); n != 2 {
		t.Fatalf("history has %d messages after a query", n)
	}
}
