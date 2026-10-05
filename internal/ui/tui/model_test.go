package tui

import (
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
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// harness drives the TUI's model over an App whose model replies as
// scripted, the way the program would.
type harness struct {
	t    *testing.T
	app  *app.App
	m    *Model
	msgs chan tea.Msg
}

func boot(t *testing.T, replies ...litellmtest.Reply) *harness {
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
		if msg := message(a, ev); msg != nil {
			h.msgs <- msg
		}
	})
	t.Cleanup(unsubscribe)
	h.feed(tea.WindowSizeMsg{Width: 80, Height: 30})
	return h
}

// feed gives the model msg and carries out the commands it returns.
func (h *harness) feed(msg tea.Msg) {
	h.do(h.m.update(msg))
}

// do carries out cmd. What it returns at once goes to the model; what takes
// longer comes later with the App's messages.
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

// take handles what a command returned, as the program would. The
// animation's ticks are left out.
func (h *harness) take(msg tea.Msg) {
	switch msg.(type) {
	case nil, tickMsg:
		return
	}
	// A batch or a sequence, which the program carries out.
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
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

// write types text into the editor.
func (h *harness) write(text string) {
	for _, r := range text {
		h.press(string(r))
	}
}

// settle feeds the App's messages to the model until the conversation goes
// idle.
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

// await feeds the App's messages to the model until one is like want.
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

// screen renders the view without styles.
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
	h := boot(t)
	h.shows(strings.TrimSpace(bot[0]), "codebot  test", tagline, "claude-sonnet-4-5  ·  effort auto", "Tip", "for commands", "balanced")
	v := h.m.View()
	if v.Cursor == nil {
		t.Fatal("the editor has no cursor")
	}
	if lines := strings.Count(v.Content, "\n") + 1; lines != 30 {
		t.Errorf("the view has %d lines, want 30", lines)
	}

	// Narrow, the bot makes way for the lines beside it.
	h.feed(tea.WindowSizeMsg{Width: 30, Height: 20})
	if s := h.screen(); strings.Contains(s, strings.TrimSpace(bot[0])) || !strings.Contains(s, "codebot  test") {
		t.Errorf("at 30 columns:\n%s", s)
	}
}

// resumable leaves a conversation behind and opens a new one.
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

func TestPermissionPanel(t *testing.T) {
	h := boot(t)
	reply := make(chan interact.Choice, 1)
	h.feed(approveMsg{interact.Approval{Tool: "bash", Summary: "rm -rf build"}, reply})
	h.shows("Allow bash?", "rm -rf build", "Yes")
	if h.m.View().Cursor != nil {
		t.Error("the editor's cursor shows under a panel")
	}
	h.press("y")
	select {
	case c := <-reply:
		if c != interact.AllowOnce {
			t.Errorf("answered %v", c)
		}
	default:
		t.Fatal("no answer")
	}
	if h.m.top() != nil {
		t.Error("the panel stayed")
	}

	// A request withdrawn takes its panel away.
	other := make(chan<- interact.Choice, 1)
	h.feed(approveMsg{interact.Approval{Tool: "write"}, other})
	h.feed(withdrawMsg{other})
	if h.m.top() != nil {
		t.Error("the withdrawn panel stayed")
	}
}

func TestCommandMenu(t *testing.T) {
	h := boot(t)
	h.write("/he")
	h.shows("/help")
	h.press("enter")
	if h.m.top() == nil {
		t.Fatal("/help showed no panel")
	}
	h.shows("Help", "commands")
	h.press("esc")
	if h.m.top() != nil {
		t.Error("esc left the panel")
	}
	h.shows("❯ /help")
}

func TestShellLine(t *testing.T) {
	h := boot(t)
	h.write("!echo hi")
	h.press("enter")
	h.shows("! echo hi", "hi")
	if len(h.m.pending) > 0 {
		t.Error("a shell line went to the agent")
	}
}

func TestMessagesForEvents(t *testing.T) {
	h := boot(t)
	for _, c := range []struct {
		ev   app.Event
		want tea.Msg
	}{
		{app.Event{Kind: app.ModeChanged, Mode: interact.ModeTrust}, modeMsg{interact.ModeTrust}},
		{app.Event{Kind: app.SessionEvent, Session: session.Event{Kind: session.RunStarted}}, runStartedMsg{}},
		{app.Event{Kind: app.SessionEvent, Session: session.Event{Kind: session.Idle}}, idleMsg{}},
	} {
		if got := message(h.app, c.ev); got != c.want {
			t.Errorf("%v: got %#v, want %#v", c.ev.Kind, got, c.want)
		}
	}
}

func TestStopAShellLine(t *testing.T) {
	h := boot(t)
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

func TestStatusIsSetOffFromTheConversation(t *testing.T) {
	h := boot(t)
	h.m.t.Append(transcript.Print(strings.Repeat("output\n", 40)))
	h.write("!sleep 30")
	h.press("enter")
	defer func() {
		h.press("esc")
		await[shellDoneMsg](h)
	}()
	lines := strings.Split(h.screen(), "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Running sleep 30") })
	if i < 2 || strings.TrimSpace(lines[i-1]) != "" || strings.TrimSpace(lines[i-2]) == "" {
		t.Errorf("no blank line above the status:\n%s", h.screen())
	}
}
