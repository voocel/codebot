package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/tui/commands"
)

// harness drives the Model the way the program would, over an App with a
// scripted model.
type harness struct {
	t    *testing.T
	app  *app.App
	m    *Model
	msgs chan tea.Msg
}

func boot(t *testing.T, replies ...litellmtest.Reply) *harness {
	t.Helper()
	return bootIn(t, nil, replies...)
}

// bootIn writes project as the folder's settings; nil writes none.
func bootIn(t *testing.T, project map[string]any, replies ...litellmtest.Reply) *harness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEBOT_THEME", "dark")
	cwd := t.TempDir()
	settings, _ := json.Marshal(map[string]any{
		"provider":  "anthropic",
		"model":     "claude-sonnet-4-5",
		"snapshot":  false,
		"providers": map[string]any{"anthropic": map[string]any{"api_key": "test", "models": []string{"claude-sonnet-4-5"}}},
	})
	if err := os.MkdirAll(filepath.Join(home, ".codebot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codebot", "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "README.md"), []byte("# demo\n\nA demo project.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if project != nil {
		data, _ := json.Marshal(project)
		if err := os.MkdirAll(filepath.Join(cwd, ".codebot"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwd, ".codebot", "settings.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	fake := litellmtest.New(replies...)
	a, err := app.Boot(app.Options{
		Cwd:         cwd,
		Mode:        interact.ModeBalanced,
		UI:          &UI{},
		Interactive: true,
		NewModel: func(spec provider.ModelSpec) (agentcore.Model, error) {
			client, err := litellm.New(fake)
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

	h := &harness{t: t, app: a, m: newModel(a, "test"), msgs: make(chan tea.Msg, 256)}
	unsubscribe := a.Subscribe(func(ev app.Event) {
		if msg := message(ev); msg != nil {
			h.msgs <- msg
		}
	})
	t.Cleanup(unsubscribe)
	h.feed(tea.WindowSizeMsg{Width: 80, Height: 30})
	return h
}

func (h *harness) feed(msg tea.Msg) {
	h.do(h.m.update(msg))
}

// do feeds what cmd returns immediately to the model; slower results arrive
// later with the App's messages.
func (h *harness) do(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		h.take(msg)
	case <-time.After(100 * time.Millisecond):
		go func() { h.msgs <- <-done }()
	}
}

// take skips animation ticks.
func (h *harness) take(msg tea.Msg) {
	switch msg.(type) {
	case nil, tickMsg:
		return
	}
	// A batch or a sequence.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeFor[tea.Cmd]() {
		for i := range v.Len() {
			h.do(v.Index(i).Interface().(tea.Cmd))
		}
		return
	}
	h.feed(msg)
}

func (h *harness) press(keys ...string) {
	for _, k := range keys {
		h.feed(keyPress(k))
	}
}

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func (h *harness) write(text string) {
	for _, r := range text {
		h.press(string(r))
	}
}

func (h *harness) settle() {
	h.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case msg := <-h.msgs:
			h.take(msg)
			if _, ok := msg.(idleMsg); ok {
				return
			}
		case <-timeout:
			h.t.Fatal("the conversation did not go idle")
		}
	}
}

// await feeds the App's messages to the model until one of type T arrives.
func await[T tea.Msg](h *harness) {
	h.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case msg := <-h.msgs:
			h.take(msg)
			if _, ok := msg.(T); ok {
				return
			}
		case <-timeout:
			h.t.Fatalf("no %T came", *new(T))
		}
	}
}

// settleNotes feeds the App's messages briefly so results of commands still
// running can land.
func (h *harness) settleNotes() {
	h.t.Helper()
	timeout := time.After(300 * time.Millisecond)
	for {
		select {
		case msg := <-h.msgs:
			h.take(msg)
		case <-timeout:
			return
		}
	}
}

func (h *harness) screen() string {
	return ansi.Strip(h.m.View().Content)
}

func (h *harness) shows(want ...string) {
	h.t.Helper()
	s := h.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			h.t.Errorf("the screen does not show %q:\n%s", w, s)
		}
	}
}

func use(id, tool string, args any) litellmtest.Reply {
	raw, _ := json.Marshal(args)
	return litellmtest.Respond(litellm.ToolUseBlock{ID: id, Name: tool, Arguments: string(raw)})
}

func TestWelcome(t *testing.T) {
	const antennae = "▀▄    ▄▀"
	h := boot(t)
	h.shows(antennae, "codebot  test", tagline, "claude-sonnet-4-5  ·  effort auto", "balanced")
	if h.m.View().Cursor == nil {
		t.Fatal("the editor has no cursor")
	}

	// When narrow, the bot is hidden to make room for the text.
	h.feed(tea.WindowSizeMsg{Width: 30, Height: 20})
	if s := h.screen(); strings.Contains(s, antennae) || !strings.Contains(s, "codebot  test") {
		t.Errorf("at 30 columns:\n%s", s)
	}
}

// resumable leaves a finished conversation behind and opens a new one.
func resumable(t *testing.T) (h *harness, id string) {
	h = boot(t, litellmtest.Text("Sure."))
	h.write("Refactor the TUI layer")
	h.press("enter")
	h.settle()
	id = h.m.conv.ID()
	h.write("/new")
	h.press("enter")
	await[openedMsg](h)
	return h, id
}

func TestWelcomeFits(t *testing.T) {
	h, _ := resumable(t)
	for _, width := range []int{20, 40, 50, 72, 100, 160} {
		for _, height := range []int{6, 10, 14, 24, 40} {
			h.feed(tea.WindowSizeMsg{Width: width, Height: height})
			lines := strings.Split(h.m.View().Content, "\n")
			if len(lines) != height {
				t.Errorf("%dx%d: %d lines", width, height, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("%dx%d: %q is %d wide", width, height, ansi.Strip(l), w)
				}
			}
		}
	}
}

func TestResumeFromTheWelcome(t *testing.T) {
	h, id := resumable(t)
	h.shows("Recent", "Refactor the TUI layer")
	if h.m.conv.ID() == id {
		t.Fatal("/new kept the conversation")
	}
	for y, row := range strings.Split(h.screen(), "\n") {
		if x := strings.Index(row, "Refactor"); x >= 0 {
			h.feed(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			await[openedMsg](h)
			if h.m.conv.ID() != id {
				t.Errorf("the click opened %s, want %s", h.m.conv.ID(), id)
			}
			h.shows("Sure.")
			return
		}
	}
	t.Fatalf("no recent conversation:\n%s", h.screen())
}

func TestConversation(t *testing.T) {
	h := boot(t,
		use("r1", "read", map[string]any{"file_path": "README.md"}),
		litellmtest.Respond(
			litellm.ReasoningBlock{Text: "The readme is short."},
			litellm.Text("It is a **demo** project:\n\n- one file\n- one line"),
		),
	)
	h.write("what is this?")
	h.press("enter")
	h.settle()

	h.shows("❯ what is this?", "Read", "README.md", "Thought", "● It is a demo project:", "• one file", "Worked for")
	if h.m.run.active || len(h.m.pending) > 0 {
		t.Errorf("after the run: active %v, pending %v", h.m.run.active, h.m.pending)
	}
	if !h.m.editor.Empty() {
		t.Error("the editor kept the input")
	}
	t.Log("\n" + h.screen())
}

func TestStopRestoresQueuedInput(t *testing.T) {
	h := boot(t, litellmtest.Reply{Blocks: []litellm.Block{litellm.Text("Looking")}, Stall: true})
	h.write("first")
	h.press("enter")
	await[runStartedMsg](h)

	h.write("second")
	h.press("enter")
	h.shows("↳ second", "esc to stop")

	h.press("esc")
	h.settle()
	if h.m.editor.Empty() || len(h.m.pending) > 0 {
		t.Errorf("the queued input did not go back to the editor: pending %v", h.m.pending)
	}
	h.shows("Interrupted", "❯ second")
}

// pause waits out armDelay so the top request takes keys.
func (h *harness) pause() {
	h.m.shownAt = h.m.shownAt.Add(-armDelay)
	h.m.lastKey = h.m.lastKey.Add(-armDelay)
}

func answered[T any](t *testing.T, reply chan T) T {
	t.Helper()
	select {
	case c := <-reply:
		return c
	default:
		t.Fatal("no answer")
		return *new(T)
	}
}

// The trust question is asked once; until trusted, the footer says so.
func TestTrustPanel(t *testing.T) {
	h := bootIn(t, map[string]any{"permissions": map[string]any{"allow": []string{"Bash(make *)"}}})
	h.shows("Trust this folder?", "would turn on:", "allows", "Bash(make *)", "outside any sandbox", "1. Trust this folder")

	h.pause()
	h.press("esc")
	if top := h.m.top(); top != nil && commands.IsAsk(top) || len(h.app.Trust().Held()) == 0 {
		t.Fatal("esc did not leave the folder untrusted")
	}
	h.shows("folder untrusted · /trust")
	h.feed(reloadedMsg{})
	if top := h.m.top(); top != nil && commands.IsAsk(top) {
		t.Fatal("the folder asks again about what the user was asked about")
	}

	h.write("/trust")
	h.press("enter")
	h.shows("Trust this folder?", "Bash(make *)")
	h.pause()
	h.press("1")
	await[reloadedMsg](h)
	if held := h.app.Trust().Held(); len(held) > 0 {
		t.Fatalf("still held: %v", held)
	}
	if strings.Contains(h.screen(), "untrusted") {
		t.Errorf("the trusted folder shows as untrusted:\n%s", h.screen())
	}
}

// A trusted folder that gains new surface asks about the new items only.
func TestTrustAsksAboutWhatIsNew(t *testing.T) {
	h := bootIn(t, map[string]any{"permissions": map[string]any{"allow": []string{"Bash(make *)"}}})
	h.pause()
	h.press("1")
	await[reloadedMsg](h)

	settings := filepath.Join(h.app.Cwd(), ".codebot", "settings.json")
	if err := os.WriteFile(settings, []byte(`{"permissions": {"allow": ["Bash(make *)", "Bash(go *)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	await[reloadedMsg](h)
	h.shows("has more to turn on since you trusted it:", "Bash(go *)")
	if strings.Contains(h.screen(), "Bash(make *)") {
		t.Errorf("asks again about what is trusted:\n%s", h.screen())
	}
	h.pause()
	h.press("esc")
	h.shows("1 waiting for trust · /trust")
}

// An unchecked item is declined and not asked about again; the rest takes
// effect.
func TestTrustPanelDeclinesWhatIsUnchecked(t *testing.T) {
	h := bootIn(t, map[string]any{"permissions": map[string]any{"allow": []string{"Bash(make *)", "Bash(rm *)"}}})
	h.shows("[x] allows", "Bash(rm *)", "space check")
	h.pause()
	h.press("up", " ")
	h.shows("[ ] allows")
	h.press("1")
	await[reloadedMsg](h)
	trust := h.app.Trust()
	if len(trust.Agreed) != 1 || trust.Agreed[0].Detail != "Bash(make *)" || len(trust.Declined) != 1 || len(trust.Ask()) > 0 {
		t.Fatalf("trust %+v", trust)
	}
	h.settleNotes()
	h.shows("but for 1 you declined")
	if top := h.m.top(); top != nil && commands.IsAsk(top) {
		t.Error("asked again about what was declined")
	}
}

// /plugins add shows what the plugin runs, to take or leave as a whole.
func TestAddAPlugin(t *testing.T) {
	h := boot(t)
	dir := filepath.Join(os.Getenv("HOME"), "kit")
	for name, text := range map[string]string{
		"plugin.json":             `{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "kit", "version": "0.1.0"}`,
		"skills/release/SKILL.md": "---\ndescription: releases\n---\nRelease.\n",
		"mcp.json":                `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {"db": {"type": "stdio", "command": "db-mcp"}}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.write("/plugins add ~/kit")
	h.press("enter")
	h.shows("Add kit 0.1.0?", "~/kit brings 1 skill · 1 MCP, and would run:", "MCP server", "kit_db: db-mcp", "1. Add")
	if strings.Contains(h.screen(), "[x]") {
		t.Error("a plugin's items are checked one by one")
	}
	h.pause()
	h.press("1")
	await[reloadedMsg](h)
	h.settleNotes()
	h.shows("❯ /plugins add ~/kit", "Added kit")

	h.write("/plugins")
	h.press("enter")
	h.shows("Plugins", "kit 0.1.0", "1 skill · 1 MCP · ~/kit · on")
}

func TestPermissionPanel(t *testing.T) {
	h := boot(t)
	reply := make(chan interact.Verdict, 1)
	h.feed(approveMsg{interact.Approval{Tool: "bash", Summary: "make build"}, reply})
	h.shows("Allow Bash?", "make build", "Yes")
	if h.m.View().Cursor != nil {
		t.Error("the editor's cursor shows under a panel")
	}

	// Keys typed as the request appeared go to the editor.
	h.write("12")
	if len(reply) > 0 || h.m.editor.Empty() {
		t.Fatalf("typing answered %d, and the editor is empty: %v", len(reply), h.m.editor.Empty())
	}
	h.pause()
	h.press("1")
	if c := answered(t, reply).Choice; c != interact.AllowOnce {
		t.Errorf("answered %v", c)
	}
	if h.m.top() != nil {
		t.Error("the panel stayed")
	}

	// Withdrawing a request removes its panel.
	other := make(chan<- interact.Verdict, 1)
	h.feed(approveMsg{interact.Approval{Tool: "write"}, other})
	h.feed(withdrawMsg{other})
	if h.m.top() != nil {
		t.Error("the withdrawn panel stayed")
	}

	// Allowing all edits switches to the accept-edits mode.
	reply = make(chan interact.Verdict, 1)
	h.feed(approveMsg{interact.Approval{Tool: "edit", Summary: "a.go", Edit: true}, reply})
	h.shows("Allow Edit?", "allow all edits")
	h.pause()
	h.press("2")
	if c := answered(t, reply).Choice; c != interact.AllowOnce || h.app.Mode() != interact.ModeAcceptEdits {
		t.Errorf("answered %v in mode %v", c, h.app.Mode())
	}
}

// A run waiting for an approval leaves the asking to its panel, with no
// status and no esc to stop it, which the panel takes; its call waits
// rather than runs.
func TestAWaitForApproval(t *testing.T) {
	h := boot(t)
	h.feed(runStartedMsg{})
	h.feed(agentMsg{agentcore.ToolStart{Call: agentcore.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"git diff"}`)}}})
	reply := make(chan interact.Verdict, 1)
	h.feed(approveMsg{interact.Approval{ToolID: "c1", Tool: "bash", Summary: "git diff"}, reply})
	h.shows("Bash(git diff) · waiting for approval", "esc deny")
	s := h.screen()
	if strings.Contains(s, "esc to stop") || strings.Contains(s, "Running…") || strings.Contains(s, "Waiting") {
		t.Errorf("the status runs on:\n%s", s)
	}
	lines := strings.Split(s, "\n")
	if i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Allow Bash?") }); i < 1 || strings.TrimSpace(lines[i-1]) != "" {
		t.Errorf("no blank line sets the panel off from the chat:\n%s", s)
	}

	h.pause()
	h.press("1")
	answered(t, reply)
	h.feed(approvedMsg{"c1"})
	h.shows("Running…", "esc to stop")
	if s = h.screen(); strings.Contains(s, "waiting") {
		t.Errorf("the call still waits:\n%s", s)
	}
	lines = strings.Split(s, "\n")
	if i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Running…") }); i < 1 || strings.TrimSpace(lines[i-1]) != "" || !strings.HasPrefix(lines[i+1], "───") {
		t.Errorf("the status is not between a blank line and the editor:\n%s", s)
	}
}

// The enter that answered one request must not also answer the next.
func TestRequestsQueue(t *testing.T) {
	h := boot(t)
	first, second := make(chan interact.Verdict, 1), make(chan interact.Verdict, 1)
	h.feed(approveMsg{interact.Approval{Tool: "bash", Summary: "make one"}, first})
	h.feed(approveMsg{interact.Approval{Tool: "bash", Summary: "make two"}, second})
	h.shows("make one", "1 more")
	h.pause()
	h.press("enter")
	answered(t, first)
	h.shows("make two")
	h.press("enter")
	if len(second) > 0 {
		t.Fatal("the enter that answered the first answered the second")
	}
	h.pause()
	h.press("esc")
	if c := answered(t, second).Choice; c != interact.Deny {
		t.Errorf("answered %v", c)
	}
}

func TestStopAShellLine(t *testing.T) {
	h := boot(t)
	h.write("!echo hi")
	h.press("enter")
	h.shows("! echo hi", "hi")
	if len(h.m.pending) > 0 {
		t.Error("a shell line went to the agent")
	}

	h.write("!sleep 30")
	h.press("enter")
	h.shows("Running sleep 30", "esc to stop")

	// Another while it runs waits in the editor.
	h.write("!echo again")
	h.press("enter")
	if h.m.editor.Empty() {
		t.Error("the second line was dropped")
	}
	h.press("ctrl+c") // clears the input
	h.press("esc")
	await[shellDoneMsg](h)
	h.shows("Stopped")
}

func TestCompactionOutsideARun(t *testing.T) {
	h := boot(t)
	h.feed(agentMsg{agentcore.CompactionStart{}})
	s := h.screen()
	if !strings.Contains(s, "Compacting the conversation · 0s") || strings.Contains(s, "esc to stop") {
		t.Errorf("status:\n%s", s)
	}
}

func TestShortScreen(t *testing.T) {
	h := boot(t)
	h.m.pending = []pending{{id: 1, text: "a"}, {id: 2, text: "b"}, {id: 3, text: "c"}}
	h.write(strings.Repeat("line\n", 9))
	h.feed(tea.WindowSizeMsg{Width: 60, Height: 10})
	v := h.m.View()
	if n := strings.Count(v.Content, "\n") + 1; n != 10 {
		t.Errorf("%d lines on a 10-line screen", n)
	}
	if v.Cursor == nil || v.Cursor.Y < 0 || v.Cursor.Y >= 10 {
		t.Fatalf("cursor at %+v", v.Cursor)
	}
	if row := strings.Split(ansi.Strip(v.Content), "\n")[v.Cursor.Y]; !strings.HasPrefix(row, "  ") && !strings.HasPrefix(row, "❯") {
		t.Errorf("the cursor is on %q, not the editor", row)
	}
}

func TestPendingInputsJoinInAnyOrder(t *testing.T) {
	h := boot(t)
	h.m.pending = []pending{{id: 1, text: "first", posted: true}, {id: 2, text: "second", posted: true}}
	h.feed(agentMsg{agentcore.MessageEnd{Message: agentcore.UserText("second")}})
	if len(h.m.pending) != 1 || h.m.pending[0].text != "first" {
		t.Errorf("pending = %v", h.m.pending)
	}
	skill := agentcore.User(litellm.Text("/review"), litellm.Text("the skill's prompt"))
	skill.Kind = "skill"
	h.feed(agentMsg{agentcore.MessageEnd{Message: skill}})
	if len(h.m.pending) != 1 {
		t.Errorf("a skill's line took an input's place: %v", h.m.pending)
	}
}

// Trusting a distrusted folder again via /trust trusts all of it, since
// nothing was declined item by item.
func TestTrustAgainAfterDistrust(t *testing.T) {
	h := bootIn(t, map[string]any{"permissions": map[string]any{"allow": []string{"Bash(make *)"}}})
	if _, err := h.app.DenyTrust(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.m.remove(commands.IsAsk)
	h.write("/trust")
	h.press("enter")
	h.shows("is not trusted. Trusted, it would turn on:", "[x] allows")
	h.pause()
	h.press("1")
	await[reloadedMsg](h)
	if trust := h.app.Trust(); trust.Denied || len(trust.Held()) > 0 {
		t.Errorf("trust %+v", trust)
	}
}
