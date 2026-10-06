package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
)

const timeout = 10 * time.Second

// call is what the model received in one call.
type call struct {
	system   []string
	msgs     []agentcore.Message
	tools    []string
	thinking *litellm.Thinking
	cache    []string // the TTL of each cache breakpoint, in order
}

// fakeModel is a provider answering each call with the next scripted reply,
// then "done".
type fakeModel struct {
	mu          sync.Mutex
	replies     []litellmtest.Reply
	calls       []call
	cannotDefer bool // the vendor takes no deferred tools
}

func script(replies ...litellmtest.Reply) *fakeModel { return &fakeModel{replies: replies} }

func (m *fakeModel) Name() string { return "fake" }

// Capabilities has deferred tools loaded on reference and reasoning
// efforts, as Anthropic does.
func (m *fakeModel) Capabilities() litellm.Capabilities {
	return litellm.Capabilities{DeferredTools: !m.cannotDefer, ThinkingEffort: true}
}

func (m *fakeModel) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	return litellmtest.New(m.next(req)).Chat(ctx, req)
}

func (m *fakeModel) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	return litellmtest.New(m.next(req)).Stream(ctx, req)
}

func (m *fakeModel) next(req *litellm.Request) litellmtest.Reply {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := call{thinking: req.Thinking}
	for _, msg := range req.Messages {
		for _, b := range msg.Blocks {
			switch b := b.(type) {
			case litellm.TextBlock:
				if b.Cache != nil {
					c.cache = append(c.cache, b.Cache.TTL)
				}
			case litellm.ToolResultBlock:
				if b.Cache != nil {
					c.cache = append(c.cache, b.Cache.TTL)
				}
			}
		}
		if msg.Role == litellm.RoleSystem {
			for _, b := range msg.Blocks {
				c.system = append(c.system, b.(litellm.TextBlock).Text)
			}
		} else {
			c.msgs = append(c.msgs, agentcore.Message{Role: msg.Role, Blocks: msg.Blocks})
		}
	}
	for _, t := range req.OfferedTools() {
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

func (u *fakeUI) Approve(_ context.Context, req interact.Approval) (interact.Verdict, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.approvals = append(u.approvals, req)
	return interact.Verdict{Choice: u.choice}, nil
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
	project  map[string]any // the project's settings
	git      bool
	cacheTTL string   // the frontend's, see Options.CacheTTL
	plugins  []string // given on the command line, see Options.PluginDirs
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

	if s.project != nil {
		writeJSON(t, config.ProjectSettingsPath(cwd), s.project)
	}

	e := &env{t: t, cwd: cwd, ui: &fakeUI{choice: interact.AllowOnce}, models: models, idle: make(chan struct{}, 64)}
	a, err := Boot(Options{
		Cwd:         cwd,
		Mode:        s.mode,
		UI:          e.ui,
		Interactive: true,
		CacheTTL:    s.cacheTTL,
		PluginDirs:  s.plugins,
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

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
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
// "tool:w1" ("tool:w1!" for an error). The messages telling the context are
// left out; see told.
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
		case strings.HasPrefix(m.Kind, kindContext):
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

// told returns the keys of the context parts msgs tell, in order.
func told(msgs []agentcore.Message) []string {
	var keys []string
	for _, m := range msgs {
		if key, ok := strings.CutPrefix(m.Kind, kindContext); ok {
			keys = append(keys, key)
		}
	}
	return keys
}

// extends reports whether the call cur starts with the call prev: the same
// system prompt, then prev's messages.
func extends(cur, prev call) bool {
	if !slices.Equal(cur.system, prev.system) || len(cur.msgs) < len(prev.msgs) {
		return false
	}
	for i, m := range prev.msgs {
		if cur.msgs[i].Role != m.Role || cur.msgs[i].Text() != m.Text() {
			return false
		}
	}
	return true
}

// The context is told again only as it changes, after what was told
// before: every request starts with the one before it.
func TestContextChangesAreAppended(t *testing.T) {
	model := script()
	e := boot(t, setup{git: true}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	c := e.app.Current()

	e.submit("one")
	e.submit("two")
	if got, want := told(c.History()), []string{"environment", "skills", "memory", "git", "tools"}; !slices.Equal(got, want) {
		t.Fatalf("told %q, want %q", got, want)
	}

	if err := os.WriteFile(filepath.Join(e.cwd, "AGENTS.md"), []byte("project rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.submit("three")
	// The new file, and the git status that shows it.
	if got := told(c.History())[5:]; !slices.Equal(got, []string{"project", "git"}) {
		t.Fatalf("told after the reload %q", got)
	}
	for i := 1; i < model.count(); i++ {
		if !extends(model.call(i), model.call(i-1)) {
			t.Fatalf("call %d does not extend call %d", i, i-1)
		}
	}
}

// A resumed conversation has the requests it had, and is told what changed
// since.
func TestResumeTellsWhatChanged(t *testing.T) {
	model := script()
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	e.submit("hi")
	if err := os.WriteFile(filepath.Join(e.cwd, "AGENTS.md"), []byte("project rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	resumed, err := e.app.Open(e.app.Current().ID())
	if err != nil {
		t.Fatal(err)
	}
	e.submit("again")

	if got, want := told(resumed.History()), []string{"environment", "skills", "memory", "tools", "project"}; !slices.Equal(got, want) {
		t.Fatalf("told %q, want %q", got, want)
	}
	if !extends(model.call(1), model.call(0)) {
		t.Fatal("the resumed conversation does not extend the request it made")
	}
}

// A side question is asked as the conversation's calls are, thinking
// included, so it reads the conversation from the prompt cache, and adds
// nothing to it.
func TestSideCallsExtendTheConversation(t *testing.T) {
	model := script(text("hello"), text("an answer"), text("run the tests"))
	e := boot(t, setup{settings: map[string]any{"reasoning_effort": "high"}}, map[string]*fakeModel{"claude-sonnet-4-5": model})
	e.submit("hi")
	c := e.app.Current()
	n := len(c.History())

	if answer, err := c.Query(context.Background(), "what?"); err != nil || answer != "an answer" {
		t.Fatalf("query = %q, %v", answer, err)
	}
	if _, err := c.Suggest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(c.History()); got != n {
		t.Fatalf("history has %d messages after the side calls, %d before", got, n)
	}
	main := model.call(0)
	if main.thinking == nil || main.thinking.Effort != "high" {
		t.Fatalf("the conversation thinks with %+v", main.thinking)
	}
	for i := 1; i <= 2; i++ {
		side := model.call(i)
		if !reflect.DeepEqual(side.thinking, main.thinking) || !extends(side, main) {
			t.Fatalf("side call %d does not extend the conversation: thinking %+v", i, side.thinking)
		}
	}
}

// Every breakpoint takes the frontend's TTL: an hour where a person paces
// the turns.
func TestCacheTTL(t *testing.T) {
	for _, ttl := range []string{"", "1h"} {
		model := script()
		e := boot(t, setup{cacheTTL: ttl}, map[string]*fakeModel{"claude-sonnet-4-5": model})
		e.submit("hi")
		// The system prompt's breakpoint, then the prompt's.
		if got := model.last().cache; !slices.Equal(got, []string{ttl, ttl}) {
			t.Errorf("frontend %q: breakpoints %q", ttl, got)
		}
	}
}

// Context a UserPromptSubmit hook adds goes ahead of the input, in a message
// of its own.
func TestHookContextGoesAheadOfTheInput(t *testing.T) {
	hooks := map[string]any{"UserPromptSubmit": []map[string]any{{"type": "command", "command": `echo '{"additional_context":"be concise"}'`}}}
	e := boot(t, setup{settings: map[string]any{"hooks": hooks}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	e.submit("hi")

	history := e.app.Current().History()
	i := slices.IndexFunc(history, func(m agentcore.Message) bool { return m.Kind == kindReminder })
	if i < 0 || !strings.Contains(history[i].Text(), "be concise") || history[i+1].Text() != "hi" {
		t.Fatalf("history = %q", texts(history))
	}
}

// MCP tools join a conversation and stay: a tool its server stops offering
// fails when called.
func TestGrowTools(t *testing.T) {
	tool := func(name, version string) agentcore.Tool {
		return agentcore.Tool{Name: name, Description: version, Run: func(context.Context, json.RawMessage) (agentcore.Result, error) {
			return agentcore.TextResult(version), nil
		}}
	}
	tools := growTools(nil, []agentcore.Tool{tool("b", "1"), tool("c", "1")})
	tools = growTools(tools, []agentcore.Tool{tool("a", "2"), tool("c", "2")})

	var got []string
	for _, t := range tools {
		got = append(got, t.Name+t.Description)
	}
	if want := []string{"b1", "c2", "a2"}; !slices.Equal(got, want) {
		t.Fatalf("tools = %q, want %q", got, want)
	}
	if _, err := tools[0].Run(context.Background(), nil); err == nil {
		t.Fatal("a tool no longer offered ran")
	}
	if res, err := tools[1].Run(context.Background(), nil); err != nil || res.Text() != "2" {
		t.Fatalf("the tool offered again runs %v, %v", res, err)
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
	n := len(resumed.History())
	_ = first.Submit(context.Background(), []litellm.Block{litellm.Text("late")})
	if got := len(resumed.History()); got != n {
		t.Fatalf("closed conversation reached the open one: %d messages", got)
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
	writeSkill(t, filepath.Join(os.Getenv("HOME"), ".codebot", "skills"), "toucher", "---\ndescription: touches files\nallowed-tools: [\"Bash(touch *)\"]\n---\nTouch the file.\n")
	if _, err := e.app.Reload(context.Background()); err != nil {
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

// A vendor that takes deferred tools is sent tool_search, which loads the
// tools it is not sent, web_fetch among them; one that cannot is sent every
// tool.
func TestDeferredToolsLoadThroughToolSearch(t *testing.T) {
	for _, cannotDefer := range []bool{false, true} {
		model := script()
		model.cannotDefer = cannotDefer
		e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": model})

		e.submit("hi")

		tools := model.last().tools
		search, fetch := slices.Contains(tools, "tool_search"), slices.Contains(tools, "web_fetch")
		if search == cannotDefer || fetch != cannotDefer {
			t.Errorf("cannot defer %v: tools sent = %q", cannotDefer, tools)
		}
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
	layers, err := config.Load(e.cwd)
	if err != nil {
		t.Fatal(err)
	}
	if m := layers.User.Model; m == nil || *m != "claude-opus-4-5" {
		t.Fatalf("the user's model = %v, want the new model remembered", m)
	}

	// The session records the model it ran on; resuming picks it up.
	resumed, err := e.app.Open(c.ID())
	if err != nil {
		t.Fatal(err)
	}
	if st := resumed.Status(); st.Model != "claude-opus-4-5" {
		t.Fatalf("resumed model = %q", st.Model)
	}

	// A new session starts on it too, as it would in a new process.
	fresh, err := e.app.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if st := fresh.Status(); st.Model != "claude-opus-4-5" {
		t.Fatalf("new session model = %q, want the remembered model", st.Model)
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
	e.submit("hi")

	dir, err := c.EnterWorktree("try")
	if err != nil {
		t.Fatal(err)
	}
	if c.Cwd() != dir || c.Worktree() != dir {
		t.Fatalf("cwd = %q, worktree = %q, want %q", c.Cwd(), c.Worktree(), dir)
	}
	e.submit("again")
	// The model is told of the move after what it was told before.
	history := c.History()
	if got := told(history)[5:]; !slices.Equal(got, []string{"environment", "git"}) {
		t.Fatalf("told after the move %q", got)
	}
	if moved := history[len(history)-4].Text(); !strings.Contains(moved, "Working directory: "+dir) {
		t.Fatalf("the environment does not state the worktree: %q", moved)
	}
	if !extends(model.call(1), model.call(0)) {
		t.Fatal("the move changed the requests made before it")
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
	writeSkill(t, filepath.Join(os.Getenv("HOME"), ".codebot", "skills"), "marked", "---\ndescription: only where marker.txt is\npaths: [marker.txt]\n---\nbody\n")
	// The marker is in the workspace, but not in git: a worktree has none.
	if err := os.WriteFile(filepath.Join(e.cwd, "marker.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reload := func() {
		if _, err := e.app.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	reload()
	c := e.app.Current()
	active := func() bool {
		return slices.ContainsFunc(c.Skills(), func(s Skill) bool { return s.Name == "marked" })
	}
	if !active() {
		t.Fatal("skill is not active in the workspace holding its marker")
	}

	dir, err := c.EnterWorktree("try")
	if err != nil {
		t.Fatal(err)
	}
	if active() {
		t.Fatal("skill is active in a worktree without its marker")
	}

	// What is active is looked up as the conversation moves or reloads, as
	// what the model is told is.
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reload()
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
