package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func mustModel(t *testing.T, tm tea.Model) *Model {
	t.Helper()
	model, ok := tm.(*Model)
	if !ok {
		t.Fatalf("expected *Model, got %T", tm)
	}
	return model
}

func TestViewShowsLiveThinkingWhenStreaming(t *testing.T) {
	m := testModel("test-model")
	m.Ready = true
	m.Width = 80
	m.IsStream = true
	m.Streaming.WriteString("assistant reply")
	m.Thinking.WriteString("thinking trace")

	view := stripANSI(m.View())
	if !strings.Contains(view, "thinking trace") {
		t.Fatalf("expected view to contain thinking text, got: %q", view)
	}
	if !strings.Contains(view, "assistant reply") {
		t.Fatalf("expected view to contain assistant streaming text, got: %q", view)
	}
}

func TestRenderCompletionsShowsCommandPalette(t *testing.T) {
	m := testModel("test-model")
	m.commands = fakeCommands{complete: func(string) []CompletionItem {
		return []CompletionItem{{
			Name:        "model",
			Description: "Switch current model",
			Kind:        "builtin",
			Aliases:     []string{"m"},
		}}
	}}
	m.Ready = true
	m.Width = 100
	m.Input.SetValue("/mo")
	m.updateCompletions()

	view := m.renderCompletions()
	for _, want := range []string{"/model", "/m", "Switch current model", "Tab complete"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected command palette to contain %q, got: %q", want, view)
		}
	}
}

func TestEnterOnCommandCompletion(t *testing.T) {
	cases := []struct {
		name       string
		item       CompletionItem
		wantInput  string
		wantHasCmd bool
	}{
		{
			name: "arg command fills input",
			item: CompletionItem{
				Name:        "model",
				Description: "Switch the model",
				AutoExecute: false,
			},
			wantInput:  "/model ",
			wantHasCmd: false,
		},
		{
			name: "no-arg command executes immediately",
			item: CompletionItem{
				Name:        "help",
				Description: "Show help",
				AutoExecute: true,
			},
			wantInput:  "",
			wantHasCmd: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel("test-model")
			m.commands = fakeCommands{}
			m.compItems = []CompletionItem{tc.item}
			m.compActive = true
			m.compIdx = 0

			next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
			got := mustModel(t, next)
			if (cmd != nil) != tc.wantHasCmd {
				t.Fatalf("cmd presence = %v, want %v", cmd != nil, tc.wantHasCmd)
			}
			if got.Input.Value() != tc.wantInput {
				t.Fatalf("input = %q, want %q", got.Input.Value(), tc.wantInput)
			}
		})
	}
}

func TestCommandPaletteReplacesBottomContextArea(t *testing.T) {
	m := testModel("anthropic/claude-sonnet-4.6")
	m.Ready = true
	m.Width = 100
	m.Cwd = "/tmp/project"
	m.Input.SetValue("/")
	m.compItems = []CompletionItem{{
		Name:        "help",
		Description: "Show help",
		Kind:        "builtin",
		AutoExecute: true,
	}}
	m.compActive = true

	view := m.View()
	if strings.Contains(view, "project · anthropic/claude-sonnet-4.6") {
		t.Fatalf("expected context bar to be hidden while palette is active, got: %q", view)
	}
	if strings.Contains(view, "╰────────────────") && strings.Contains(view, "project · anthropic/claude-sonnet-4.6") {
		t.Fatalf("expected palette to own the bottom area, got: %q", view)
	}
}

// The status carries what the conversation changed: its model, and where it
// works, which moves into and out of a worktree.
func TestStatusChangeUpdatesModelAndCwd(t *testing.T) {
	m := testModel("gpt-4.1")
	m.Status.Provider, m.Cwd = "openai", "/tmp/project"

	nextModel, _ := m.Update(StatusChangedMsg{Status: app.Status{
		Status: session.Status{Provider: "openrouter", Model: "openai/gpt-5", Window: 400_000},
		Cwd:    "/tmp/project/.codebot/worktrees/fix",
	}})
	next := mustModel(t, nextModel)
	if st := next.Status; st.Provider != "openrouter" || st.Model != "openai/gpt-5" || st.Window != 400_000 {
		t.Fatalf("provider, model, window = %q, %q, %d", st.Provider, st.Model, st.Window)
	}
	if next.Cwd != "/tmp/project/.codebot/worktrees/fix" {
		t.Fatalf("cwd = %q", next.Cwd)
	}
}

func TestFormatScrollbackBlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		inline  bool
		want    string
	}{
		{name: "block adds leading blank line", content: "  ok", inline: false, want: "\n  ok"},
		{name: "inline stays flush", content: "  ok", inline: true, want: "  ok"},
		{name: "block strips trailing newlines", content: "hello\n\n", inline: false, want: "\nhello"},
		{name: "inline strips trailing newlines", content: "hello\n\n", inline: true, want: "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := formatScrollbackBlock(tc.content, tc.inline); got != tc.want {
				t.Fatalf("formatScrollbackBlock(%q, %v) = %q, want %q", tc.content, tc.inline, got, tc.want)
			}
		})
	}
}

// An overlay takes the input's place; a dialog, which takes the keys first,
// shows over it.
func TestOverlayReplacesInputAndDialogsShowOverIt(t *testing.T) {
	m := testModel("anthropic/claude-sonnet-4.6")
	m.commands = fakeCommands{overlay: &OverlayState{
		View: func(width, height int) string { return "overlay-body" },
	}}
	m.Ready = true
	m.Width = 100
	m.Input.SetValue("/model")

	view := m.View()
	if !strings.Contains(view, "overlay-body") || strings.Contains(view, "/model") {
		t.Fatalf("expected the overlay in place of the input, got: %q", view)
	}

	m.Update(PermissionMsg{Approval: interact.Approval{Tool: "bash", Summary: "make deploy"}, RespCh: make(chan interact.Choice, 1)})
	view = m.View()
	if !strings.Contains(view, "make deploy") || strings.Contains(view, "overlay-body") {
		t.Fatalf("expected the dialog over the overlay, got: %q", view)
	}
}

func useImmediateHideCompletedTodosTick(t *testing.T) {
	t.Helper()
	orig := hideCompletedTodosTick
	hideCompletedTodosTick = func(version uint64) tea.Cmd {
		return func() tea.Msg { return hideCompletedTodosMsg{Version: version} }
	}
	t.Cleanup(func() { hideCompletedTodosTick = orig })
}

func todoEvents(id, args string, isError bool) (agentcore.Event, agentcore.Event) {
	c := call(id, todo.ToolName, args)
	res := agentcore.TextResult("ok")
	res.IsError = isError
	return agentcore.ToolStart{Call: c}, agentcore.ToolEnd{Call: c, Result: res}
}

// The list follows todo_write calls the tool accepted; a rejected call leaves
// the previous list on screen.
func TestTodoWriteEventsDriveTheList(t *testing.T) {
	m := testModel("test-model")
	start, end := todoEvents("1", `{"todos":[{"content":"read","status":"in_progress"}]}`, false)
	m.HandleAgentEvent(start)
	m.HandleAgentEvent(end)
	if len(m.Todos) != 1 || m.Todos[0].Content != "read" {
		t.Fatalf("Todos = %+v, want the accepted list", m.Todos)
	}

	start, end = todoEvents("2", `{"todos":[{"content":"x","status":"in_progress"},{"content":"y","status":"in_progress"}]}`, true)
	m.HandleAgentEvent(start)
	m.HandleAgentEvent(end)
	if len(m.Todos) != 1 || m.Todos[0].Content != "read" {
		t.Fatalf("Todos = %+v, rejected call must not replace the list", m.Todos)
	}
}

func TestCompletedTodosHideAfterDelay(t *testing.T) {
	useImmediateHideCompletedTodosTick(t)

	m := testModel("test-model")
	cmd := m.setTodos([]todo.Item{{Content: "a", Status: todo.Completed}})
	if cmd == nil {
		t.Fatal("expected a hide command for a fully completed list")
	}
	nextModel, _ := m.Update(cmd())
	if next := mustModel(t, nextModel); next.Todos != nil {
		t.Fatalf("expected the completed list to be hidden, got %+v", next.Todos)
	}
}

func TestStaleHideDoesNotClearNewOpenTodos(t *testing.T) {
	useImmediateHideCompletedTodosTick(t)

	m := testModel("test-model")
	staleHide := m.setTodos([]todo.Item{{Content: "a", Status: todo.Completed}})()
	m.setTodos([]todo.Item{{Content: "b", Status: todo.Pending}})

	nextModel, _ := m.Update(staleHide)
	if next := mustModel(t, nextModel); len(next.Todos) != 1 || next.Todos[0].Content != "b" {
		t.Fatalf("stale hide must be ignored, got %+v", next.Todos)
	}
}

func TestRestoreSchedulesHideForCompletedTodos(t *testing.T) {
	useImmediateHideCompletedTodosTick(t)

	m := testModel("test-model")
	args := `{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"completed"}]}`
	m.restored = []agentcore.Message{
		{Role: litellm.RoleAssistant, Blocks: []litellm.Block{litellm.ToolUseBlock{ID: "t1", Name: todo.ToolName, Arguments: args}}},
		agentcore.ToolResult("t1", agentcore.TextResult("ok")),
	}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if len(m.Todos) != 2 || cmd == nil {
		t.Fatalf("todos = %+v, want the restored list with its hide scheduled", m.Todos)
	}
}

// testModel is a Model with no conversation open, showing modelName.
func testModel(modelName string) *Model {
	m := newModel()
	m.commands = fakeCommands{}
	m.agents, m.tasks = app.NewAgentHub(), task.NewRuntime("", nil)
	m.history = &inputHistory{}
	m.Status.Model = modelName
	return m
}

// fakeCommands offers fixed completions and overlay.
type fakeCommands struct {
	complete func(prefix string) []CompletionItem
	overlay  *OverlayState
}

func (fakeCommands) Run(string) tea.Cmd { return nil }

func (f fakeCommands) Complete(prefix string) []CompletionItem {
	if f.complete == nil {
		return nil
	}
	return f.complete(prefix)
}

func (f fakeCommands) Overlay() *OverlayState { return f.overlay }
