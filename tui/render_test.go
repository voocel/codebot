package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

func TestIndentBlock(t *testing.T) {
	input := "line1\nline2\nline3"
	out := indentBlock(input, 4)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("line not indented: %q", line)
		}
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
