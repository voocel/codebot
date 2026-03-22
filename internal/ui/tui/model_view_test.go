package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
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

func TestCommandPaletteIdleBadgeStaysSingleLine(t *testing.T) {
	badge := CommandPaletteIdleBadge(true)
	if strings.Contains(badge, "\n") {
		t.Fatalf("expected idle badge to stay single-line, got: %q", badge)
	}
}

func TestEnterOnArgCommandOnlyFillsInput(t *testing.T) {
	m := New(nil, "test-model")
	m.compItems = []CompletionItem{{
		Name:        "model",
		Description: "切换当前模型",
		Usage:       "/model [name]",
		AutoExecute: false,
	}}
	m.compActive = true
	m.compIdx = 0

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if cmd != nil {
		t.Fatal("expected arg command enter to only fill input")
	}
	if got.Input.Value() != "/model " {
		t.Fatalf("expected input to be filled, got %q", got.Input.Value())
	}
}

func TestEnterOnNoArgCommandExecutesImmediately(t *testing.T) {
	m := New(nil, "test-model")
	m.compItems = []CompletionItem{{
		Name:        "help",
		Description: "显示帮助",
		Usage:       "/help",
		AutoExecute: true,
	}}
	m.compActive = true
	m.compIdx = 0

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if cmd == nil {
		t.Fatal("expected no-arg command enter to execute immediately")
	}
	if got.Input.Value() != "" {
		t.Fatalf("expected input to be cleared after execution, got %q", got.Input.Value())
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
		Description: "显示帮助",
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

func TestOverlayAppearsBelowInput(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6", Config{
		Overlay: func(*Model) *OverlayState {
			return &OverlayState{
				View: func(width int) string {
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
				Prompt:  "Would you like to proceed?",
				Choices: []string{"Execute plan", "Cancel"},
			}
		},
	})
	m.Ready = true
	m.Width = 100
	m.Cwd = "/tmp/project"

	view := m.View()
	promptIdx := strings.Index(view, "Would you like to proceed?")
	modeIdx := strings.Index(view, "◇ plan mode")
	if promptIdx < 0 || modeIdx < 0 {
		t.Fatalf("expected plan review prompt and bottom mode indicator, got: %q", view)
	}
	if modeIdx < promptIdx {
		t.Fatalf("expected mode indicator below plan review card, got: %q", view)
	}
	if strings.Contains(view, "❯ ") {
		t.Fatalf("expected input area to stay hidden during plan review, got: %q", view)
	}
}
