package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
)

func TestWrapTextBreaksLongTokens(t *testing.T) {
	m := Model{Width: 20, Ready: true}

	input := strings.Repeat("x", 50)
	out := m.wrapTextForIndent(input, 0)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped output to span multiple lines, got %q", out)
	}
	for _, line := range lines {
		if got := lipgloss.Width(line); got > 20 {
			t.Fatalf("line width = %d, want <= 20; line=%q", got, line)
		}
	}
}

func TestWrapTextFallbackWidth(t *testing.T) {
	m := Model{Width: 0}
	out := m.wrapTextForIndent(strings.Repeat("a", 100), 0)
	// Should not panic, should use fallback width
	if out == "" {
		t.Fatal("expected non-empty output with fallback width")
	}
}

func TestRenderStatusBarRespectsTerminalWidth(t *testing.T) {
	m := Model{
		Width:     32,
		ModelName: "claude-sonnet-4-5-very-long-name",
		TurnCount: 1234,
	}

	bar := m.RenderStatusBar()
	if got := lipgloss.Width(bar); got > 32 {
		t.Fatalf("status bar width = %d, want <= 32", got)
	}
}

func TestRenderStatusBarDoesNotIncludePlanModeTag(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6", Config{
		StatusPlan: func(*Model) *PlanBarInfo {
			return &PlanBarInfo{Tag: "plan mode"}
		},
	})
	m.Ready = true
	m.Width = 100
	m.Running = true
	m.RunStats.StartedAt = time.Now().Add(-2 * time.Second)
	m.RunStats.DisplayInput = 1200
	m.RunStats.DisplayOutput = 340

	bar := m.RenderStatusBar()
	if !strings.Contains(bar, "Running") {
		t.Fatalf("expected running status in %q", bar)
	}
	if strings.Contains(bar, "plan mode") {
		t.Fatalf("expected plan mode tag to stay out of status bar, got %q", bar)
	}
}

func TestRenderContextBarShowsModeIndicator(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6", Config{
		StatusMode: func(*Model) string { return "◇ plan mode" },
	})
	m.Ready = true
	m.Width = 100
	m.Cwd = "/tmp/project"

	bar := m.RenderContextBar()
	if !strings.Contains(bar, "◇ plan mode") {
		t.Fatalf("expected mode indicator in context bar, got %q", bar)
	}
	if !strings.Contains(bar, "project") {
		t.Fatalf("expected project name in context bar, got %q", bar)
	}
}

func TestRenderPermissionKeepsBilingualOptionOnSingleLine(t *testing.T) {
	s := initPermission(PermissionMsg{
		Tool:    "bash",
		Command: "echo hello",
		Reason:  "shell execution requires approval",
	})

	view := renderPermission(s)
	if !strings.Contains(view, "1. Allow once") {
		t.Fatalf("expected first option in permission view, got %q", view)
	}
	if !strings.Contains(view, "仅本次允许") {
		t.Fatalf("expected Chinese hint in permission view, got %q", view)
	}
	if !strings.Contains(view, "(仅本次允许)") {
		t.Fatalf("expected Chinese hint to be wrapped in parentheses, got %q", view)
	}
	if strings.Contains(view, "1. Allow once\n") {
		t.Fatalf("expected bilingual label to stay on one line, got %q", view)
	}
}

func TestFormatToolOutputHighlightsDiffStatPaths(t *testing.T) {
	out := FormatToolOutput("internal/ui/tui/render.go | 38 +++++++++\ninternal/ui/tui/model.go | 25 +++++", 5)
	if !strings.Contains(out, ToolPathStyle.Render("internal/ui/tui/render.go")) {
		t.Fatalf("expected diff stat path highlight, got %q", out)
	}
	if !strings.Contains(out, ToolPathStyle.Render("internal/ui/tui/model.go")) {
		t.Fatalf("expected second diff stat path highlight, got %q", out)
	}
}

func TestFormatToolOutputHighlightsLsPathsAndFiles(t *testing.T) {
	text := "/Users/voocel/project/me/codebot/internal/ui/tui/\nask_user.go 12.7KB\nmarkdown/"
	out := FormatToolOutput(text, 5)
	for _, want := range []string{
		ToolPathStyle.Render("/Users/voocel/project/me/codebot/internal/ui/tui/"),
		ToolPathStyle.Render("ask_user.go"),
		ToolPathStyle.Render("markdown/"),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected highlighted path token %q, got %q", want, out)
		}
	}
}

func TestRenderMarkdownAddsAnsiStyling(t *testing.T) {
	m := New(nil, "test-model")
	m.Width = 100
	m.Markdown = markdown.NewRenderer(96)

	rendered := m.RenderMarkdown("# Title\n\n- item\n\n`这种` *这种* **这种**")
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected markdown output to contain ANSI styling, got %q", rendered)
	}
	plain := stripANSI(rendered)
	for _, want := range []string{"Title", "item", "这种"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered markdown to preserve content %q, got %q", want, plain)
		}
	}
}

func TestRenderMarkdownBlockPreservesMarkdownStructure(t *testing.T) {
	m := New(nil, "test-model")
	m.Width = 100
	m.Markdown = markdown.NewRenderer(96)

	block := m.renderMarkdownBlock("# Title\n\n- first\n- second", 2)
	lines := strings.Split(block, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected multiline markdown block, got %q", block)
	}
}

func TestRenderMarkdownBlockFormatsContent(t *testing.T) {
	m := New(nil, "test-model")
	m.Width = 100
	m.Markdown = markdown.NewRenderer(96)

	block := m.renderMarkdownBlock("# Title\n\n- item", 2)
	plain := stripANSI(block)
	if !strings.Contains(plain, "Title") || !strings.Contains(plain, "item") {
		t.Fatalf("expected markdown content to stay visible, got %q", plain)
	}
}

func TestRenderStatusBarHiddenWhilePermissionPromptActive(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6")
	m.Ready = true
	m.Width = 100
	m.Running = true
	m.RunStats.StartedAt = time.Now().Add(-2 * time.Second)
	m.Permission = initPermission(PermissionMsg{
		Tool:    "bash",
		Command: "echo hello",
		Reason:  "shell execution requires approval",
	})

	if bar := m.RenderStatusBar(); bar != "" {
		t.Fatalf("expected status bar to hide while permission prompt is active, got %q", bar)
	}
}

func TestRenderStatusBarHiddenWhilePlanReviewAwaitsChoice(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6", Config{
		StatusPlan: func(*Model) *PlanBarInfo {
			return &PlanBarInfo{
				Prompt:  "Would you like to proceed?",
				Choices: []string{"Execute plan", "Cancel"},
			}
		},
	})
	m.Ready = true
	m.Width = 100
	m.Running = true
	m.RunStats.StartedAt = time.Now().Add(-2 * time.Second)

	if bar := m.RenderStatusBar(); bar != "" {
		t.Fatalf("expected status bar to hide while waiting for plan review choice, got %q", bar)
	}
}

func TestIndentBlock(t *testing.T) {
	input := "line1\nline2\nline3"
	out := indentBlock(input, 4)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("line not indented: %q", line)
		}
	}
}

func TestFormatProgressLineUsesStructuredPayload(t *testing.T) {
	line := FormatProgressLine(&agentcore.ProgressPayload{
		Kind: agentcore.ProgressToolStart,
		Tool: "read",
		Args: []byte(`{"path":"internal/ui/tui/events.go"}`),
	})
	plain := stripANSI(line)
	if !strings.Contains(plain, "read") || !strings.Contains(plain, "internal/ui/tui/events.go") {
		t.Fatalf("expected tool progress line to include tool and hint, got %q", plain)
	}
}

func TestFormatProgressLineSummary(t *testing.T) {
	line := FormatProgressLine(&agentcore.ProgressPayload{
		Kind:    agentcore.ProgressSummary,
		Summary: "foreground output line",
	})
	if got := stripANSI(line); got != "foreground output line" {
		t.Fatalf("summary line = %q, want %q", got, "foreground output line")
	}
}

func TestIndentBlockSkipsEmptyLines(t *testing.T) {
	input := "line1\n\nline3"
	out := indentBlock(input, 2)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[1] != "" {
		t.Fatalf("empty line should stay empty, got %q", lines[1])
	}
}

func TestIndentBlockEmpty(t *testing.T) {
	if got := indentBlock("", 4); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestShortenPath(t *testing.T) {
	result := shortenPath("/some/random/path")
	if result != "/some/random/path" {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestRenderStreamingOutputTruncates(t *testing.T) {
	full := "line1\nline2\nline3\nline4\nline5\nline6\nline7\n"
	out := RenderStreamingOutput(full, 3)
	if !strings.Contains(out, "lines above") {
		t.Fatal("expected truncation indicator for >3 lines")
	}
}
