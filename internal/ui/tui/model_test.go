package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/storage"
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

func useImmediateHideCompletedTasksTick(t *testing.T) {
	t.Helper()
	orig := hideCompletedTasksTick
	hideCompletedTasksTick = func(version uint64) tea.Cmd {
		return func() tea.Msg { return HideCompletedTasksMsg{Version: version} }
	}
	t.Cleanup(func() {
		hideCompletedTasksTick = orig
	})
}

func TestViewShowsLiveThinkingWhenStreaming(t *testing.T) {
	m := New(nil, "test-model")
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
	m := New(nil, "test-model", Config{
		Completions: func(prefix string) []CompletionItem {
			return []CompletionItem{
				{
					Name:        "model",
					Description: "Switch current model",
					Usage:       "/model [name]",
					Kind:        "builtin",
					Category:    "config",
					NeedsIdle:   true,
					Aliases:     []string{"m"},
					AutoExecute: false,
				},
			}
		},
	})
	m.Ready = true
	m.Width = 100
	m.Input.SetValue("/mo")
	m.updateCompletions()

	view := m.renderCompletions()
	for _, want := range []string{"Commands", "/model", "Usage: /model [name]", "Aliases: /m", "Switch current model"} {
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
				Name:        "plan",
				Description: "Enter plan mode",
				Usage:       "/plan [cancel|<task>]",
				AutoExecute: false,
			},
			wantInput:  "/plan ",
			wantHasCmd: false,
		},
		{
			name: "no-arg command executes immediately",
			item: CompletionItem{
				Name:        "help",
				Description: "Show help",
				Usage:       "/help",
				AutoExecute: true,
			},
			wantInput:  "",
			wantHasCmd: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil, "test-model")
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
	m := New(nil, "anthropic/claude-sonnet-4.6")
	m.Ready = true
	m.Width = 100
	m.Cwd = "/tmp/project"
	m.Input.SetValue("/")
	m.compItems = []CompletionItem{{
		Name:        "help",
		Description: "Show help",
		Usage:       "/help",
		Kind:        "builtin",
		Category:    "info",
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

func TestHandleCommandResultUpdatesProviderAndModel(t *testing.T) {
	m := New(nil, "gpt-4.1")
	m.Provider = "openai"

	nextModel, _ := m.handleCommandResult(CommandResultMsg{
		NewProvider: "openrouter",
		NewModel:    "openai/gpt-5",
	})
	next := mustModel(t, nextModel)
	if next.Provider != "openrouter" {
		t.Fatalf("provider = %q, want %q", next.Provider, "openrouter")
	}
	if next.ModelName != "openai/gpt-5" {
		t.Fatalf("model = %q, want %q", next.ModelName, "openai/gpt-5")
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

func TestOverlayAppearsBelowInput(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6", Config{
		Overlay: func(*Model) *OverlayState {
			return &OverlayState{
				View: func(width, height int) string {
					return "overlay-body"
				},
			}
		},
	})
	m.Ready = true
	m.Width = 100
	m.Cwd = "/tmp/project"
	m.Input.SetValue("/model")

	view := m.View()
	inputIdx := strings.Index(view, "/model")
	overlayIdx := strings.Index(view, "overlay-body")
	if inputIdx < 0 || overlayIdx < 0 {
		t.Fatalf("expected both input and overlay to render, got: %q", view)
	}
	if inputIdx > overlayIdx {
		t.Fatalf("expected overlay below input, got: %q", view)
	}
	if strings.Contains(view, "project · anthropic/claude-sonnet-4.6") {
		t.Fatalf("expected context bar to stay hidden while overlay is active, got: %q", view)
	}
}

func TestPlanReviewKeepsPlanModeInBottomContextBar(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6", Config{
		StatusMode: func(*Model) string { return "◇ plan mode" },
		StatusPlan: func(*Model) *PlanBarInfo {
			return &PlanBarInfo{
				Title:   "Refactor session manager",
				Details: []string{"Allowed command prefixes:", "- go test — run tests"},
				Choices: []string{"Execute plan", "Cancel"},
			}
		},
	})
	m.Ready = true
	m.Width = 100
	m.Cwd = "/tmp/project"

	view := m.View()
	promptIdx := strings.Index(view, "Ready to code?")
	modeIdx := strings.Index(view, "◇ plan mode")
	if promptIdx < 0 || modeIdx < 0 {
		t.Fatalf("expected plan review heading and bottom mode indicator, got: %q", view)
	}
	if !strings.Contains(view, "Allowed command prefixes:") || !strings.Contains(view, "go test") {
		t.Fatalf("expected allowed command details, got: %q", view)
	}
	if modeIdx < promptIdx {
		t.Fatalf("expected mode indicator below plan review card, got: %q", view)
	}
	if strings.Contains(view, "❯ ") {
		t.Fatalf("expected input area to stay hidden during plan review, got: %q", view)
	}
}

func TestTaskListUpdateSchedulesHideWhenAllCompleted(t *testing.T) {
	useImmediateHideCompletedTasksTick(t)

	m := New(nil, "test-model")
	nextModel, cmd := m.Update(TaskListUpdateMsg{
		Snapshot: storage.TaskSnapshot{
			Completed: 1,
			Total:     1,
		},
	})
	next := mustModel(t, nextModel)

	if next.Tasks == nil || next.Tasks.Total != 1 || next.Tasks.Completed != 1 {
		t.Fatalf("expected completed task snapshot to be kept before hiding, got %#v", next.Tasks)
	}
	if next.taskHideVersion != 1 {
		t.Fatalf("taskHideVersion = %d, want 1", next.taskHideVersion)
	}
	if cmd == nil {
		t.Fatal("expected hide command for fully completed tasks")
	}
	hideMsg, ok := cmd().(HideCompletedTasksMsg)
	if !ok {
		t.Fatalf("expected HideCompletedTasksMsg, got %T", cmd())
	}
	if hideMsg.Version != 1 {
		t.Fatalf("hide version = %d, want 1", hideMsg.Version)
	}
}

func TestHideCompletedTasksMsgRunsHideCallback(t *testing.T) {
	m := New(nil, "test-model", Config{
		OnHideCompletedTasks: func(snap storage.TaskSnapshot) tea.Cmd {
			return func() tea.Msg {
				if snap.Total != 1 || snap.Completed != 1 {
					t.Fatalf("unexpected snapshot passed to hide callback: %#v", snap)
				}
				return CommandResultMsg{Text: "hidden"}
			}
		},
	})
	snap := storage.TaskSnapshot{
		Completed: 1,
		Total:     1,
	}
	m.Tasks = &snap
	m.taskHideVersion = 1

	nextModel, cmd := m.Update(HideCompletedTasksMsg{Version: 1})
	next := mustModel(t, nextModel)
	if next.Tasks != nil {
		t.Fatalf("expected tasks to be hidden, got %#v", next.Tasks)
	}
	if cmd == nil {
		t.Fatal("expected hide callback command")
	}
	msg := cmd()
	if _, ok := msg.(CommandResultMsg); !ok {
		t.Fatalf("expected CommandResultMsg from hide callback, got %T", msg)
	}
}

func TestHideCompletedTasksMsgDoesNotClearNewOpenTasks(t *testing.T) {
	useImmediateHideCompletedTasksTick(t)

	m := New(nil, "test-model")
	nextModel, cmd := m.Update(TaskListUpdateMsg{
		Snapshot: storage.TaskSnapshot{
			Completed: 1,
			Total:     1,
		},
	})
	staleHide := cmd().(HideCompletedTasksMsg)
	next := mustModel(t, nextModel)

	nextModel, _ = next.Update(TaskListUpdateMsg{
		Snapshot: storage.TaskSnapshot{
			Pending: 1,
			Total:   1,
		},
	})
	next = mustModel(t, nextModel)
	if next.taskHideVersion != 2 {
		t.Fatalf("taskHideVersion = %d, want 2 after new snapshot", next.taskHideVersion)
	}

	nextModel, _ = next.Update(staleHide)
	next = mustModel(t, nextModel)
	if next.Tasks == nil {
		t.Fatal("expected stale hide message to be ignored")
	}
	if next.Tasks.Pending != 1 || next.Tasks.Total != 1 {
		t.Fatalf("expected new pending task snapshot to stay visible, got %#v", next.Tasks)
	}
}

func TestInitSchedulesHideForInitiallyCompletedTasks(t *testing.T) {
	useImmediateHideCompletedTasksTick(t)

	snap := storage.TaskSnapshot{
		Completed: 2,
		Total:     2,
	}
	m := New(nil, "test-model", Config{InitialTasks: &snap})

	if m.taskHideVersion != 1 {
		t.Fatalf("taskHideVersion = %d, want 1 for initial completed snapshot", m.taskHideVersion)
	}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected init command batch")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg from Init, got %T", cmd())
	}
	if len(batch) == 0 {
		t.Fatal("expected init batch to include commands")
	}
	msg := batch[len(batch)-1]()
	hideMsg, ok := msg.(HideCompletedTasksMsg)
	if !ok {
		t.Fatalf("expected final init command to schedule HideCompletedTasksMsg, got %T", msg)
	}
	if hideMsg.Version != 1 {
		t.Fatalf("hide version = %d, want 1", hideMsg.Version)
	}
}
