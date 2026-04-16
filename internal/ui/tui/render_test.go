package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestWrapTextBreaksLongTokens(t *testing.T) {
	m := Model{State: State{Width: 20, Ready: true}}

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

func TestRenderInputPanelHighlightsShellMode(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6")
	m.Ready = true
	m.Width = 80
	m.Input.SetValue("!git status")

	if !m.shellInputActive() {
		t.Fatal("expected shell input mode to activate for !-prefixed input")
	}
}

func TestRenderInputPanelUsesDefaultStyleWithoutShellPrefix(t *testing.T) {
	m := New(nil, "anthropic/claude-sonnet-4.6")
	m.Ready = true
	m.Width = 80
	m.Input.SetValue("git status")

	if m.shellInputActive() {
		t.Fatal("did not expect shell input mode without ! prefix")
	}
}

func TestIndentBlock(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		indent int
		assert func(*testing.T, string)
	}{
		{
			name:   "indents non-empty lines",
			input:  "line1\nline2\nline3",
			indent: 4,
			assert: func(t *testing.T, out string) {
				t.Helper()
				for _, line := range strings.Split(out, "\n") {
					if !strings.HasPrefix(line, "    ") {
						t.Fatalf("line not indented: %q", line)
					}
				}
			},
		},
		{
			name:   "keeps empty lines empty",
			input:  "line1\n\nline3",
			indent: 2,
			assert: func(t *testing.T, out string) {
				t.Helper()
				lines := strings.Split(out, "\n")
				if len(lines) != 3 {
					t.Fatalf("expected 3 lines, got %d", len(lines))
				}
				if lines[1] != "" {
					t.Fatalf("empty line should stay empty, got %q", lines[1])
				}
			},
		},
		{
			name:   "empty input stays empty",
			input:  "",
			indent: 4,
			assert: func(t *testing.T, out string) {
				t.Helper()
				if out != "" {
					t.Fatalf("expected empty string, got %q", out)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.assert(t, indentBlock(tc.input, tc.indent))
		})
	}
}
