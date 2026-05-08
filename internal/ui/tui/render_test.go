package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// pinTrueColor forces lipgloss to emit SGR escapes inside this test even
// without a TTY, so bg/fg assertions are meaningful. Scope is limited to the
// caller via t.Cleanup — a package-level init would change ANSI output for
// every other test in this package and break ones that compare plain strings.
func pinTrueColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

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

// edit-tool diff format (see agentcore/tools/edit.go:892-898):
//
//	-%*d %s\n  for removed lines
//	+%*d %s\n  for added lines
//	 %*d %s\n  for context lines (leading space)
//	 %*s ...\n for truncation marker
//
// The renderer must:
//   - leave context lines untouched (no add/remove background)
//   - paint the whole row (gutter + body) on one colored band, so the line
//     number sits on the same background as the code
//   - apply foreground only to the gutter and pad the body to width so the
//     band reaches the right edge instead of stopping at the last code char
func TestRenderEditResultDiffColoring(t *testing.T) {
	pinTrueColor(t)
	diff := strings.Join([]string{
		" 1 unchanged context",
		"-2 old line",
		"+2 new line",
		" 3 trailing context",
	}, "\n")
	payload, _ := json.Marshal(map[string]any{"diff": diff})

	out := RenderEditResult(payload, 40)
	lines := strings.Split(out, "\n")
	if len(lines) < 5 {
		t.Fatalf("expected stats + 4 diff lines, got %d:\n%s", len(lines), out)
	}

	// Stats header.
	if !strings.Contains(lines[0], "Added 1 lines, removed 1 lines") {
		t.Fatalf("missing stats line: %q", lines[0])
	}

	// Context lines must not carry the diff background ANSI (red/green bg
	// codes 41/42/48). MutedStyle is foreground-only.
	for _, idx := range []int{1, 4} {
		if hasBgEscape(lines[idx]) {
			t.Fatalf("context line %d unexpectedly has background ANSI: %q", idx, lines[idx])
		}
	}

	// Removed/added lines must have a body with background ANSI.
	if !hasBgEscape(lines[2]) {
		t.Fatalf("removed line missing background: %q", lines[2])
	}
	if !hasBgEscape(lines[3]) {
		t.Fatalf("added line missing background: %q", lines[3])
	}

	// Regression guard: the bg band must cover the *whole* row, gutter
	// included — an earlier revision left the gutter on the terminal
	// default, which doesn't match Claude Code's layout. Check that a bg
	// SGR appears before any visible character on each diff line.
	for _, idx := range []int{2, 3} {
		if !startsInBgScope(lines[idx]) {
			t.Fatalf("diff line %d gutter not painted (expected bg ANSI before any visible char): %q", idx, lines[idx])
		}
	}

	// Visible width of every diff line must reach `width` so the bg band
	// fills the row. lipgloss.Width strips ANSI before counting.
	for _, idx := range []int{2, 3} {
		if got := lipgloss.Width(lines[idx]); got != 40 {
			t.Fatalf("diff line %d visible width = %d, want 40; line=%q", idx, got, lines[idx])
		}
	}
}

// renderDiffLine on a short line should pad the body to the target width so
// the background band reaches the edge instead of stopping at the last code
// character.
func TestRenderDiffLinePadsToWidth(t *testing.T) {
	pinTrueColor(t)
	out := renderDiffLine("+5 hi", DiffAddGutterStyle, DiffAddBodyStyle, 30)
	if got := lipgloss.Width(out); got != 30 {
		t.Fatalf("padded width = %d, want 30; out=%q", got, out)
	}
}

// renderDiffLine must not truncate or wrap when the line already exceeds
// width — the terminal handles wrapping. Reporting visible width >= len
// confirms we didn't drop characters.
func TestRenderDiffLineLongerThanWidthIsNotTruncated(t *testing.T) {
	pinTrueColor(t)
	long := "+5 " + strings.Repeat("x", 100)
	out := renderDiffLine(long, DiffAddGutterStyle, DiffAddBodyStyle, 20)
	if got := lipgloss.Width(out); got < lipgloss.Width(long) {
		t.Fatalf("visible width %d < input width %d, content was truncated", got, lipgloss.Width(long))
	}
}

// hasBgEscape reports whether the rendered string includes a background-color
// ANSI escape (SGR codes 40–47, 48, or 100–107). Foreground-only renderings
// (MutedStyle, gutter style) must not match.
func hasBgEscape(s string) bool {
	// Walk every SGR sequence "\x1b[...m" and inspect its parameters.
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		end := strings.IndexByte(s[i:], 'm')
		if end < 0 {
			break
		}
		params := s[i+2 : i+end]
		i += end
		for _, p := range strings.Split(params, ";") {
			switch p {
			case "48":
				return true
			}
			if len(p) == 2 && p[0] == '4' && p[1] >= '0' && p[1] <= '7' {
				return true
			}
			if len(p) == 3 && p[0] == '1' && p[1] == '0' && p[2] >= '0' && p[2] <= '7' {
				return true
			}
		}
	}
	return false
}

// startsInBgScope reports whether the first visible character of s sits
// inside an active background-color SGR scope. It walks the prefix and
// returns true iff a bg-color SGR is seen before any non-escape byte.
func startsInBgScope(s string) bool {
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			// Hit visible content; the answer depends on whether we've
			// already opened a bg scope above.
			return false
		}
		end := strings.IndexByte(s[i:], 'm')
		if end < 0 {
			return false
		}
		params := s[i+2 : i+end]
		for _, p := range strings.Split(params, ";") {
			if p == "48" {
				return true
			}
			if len(p) == 2 && p[0] == '4' && p[1] >= '0' && p[1] <= '7' {
				return true
			}
			if len(p) == 3 && p[0] == '1' && p[1] == '0' && p[2] >= '0' && p[2] <= '7' {
				return true
			}
		}
		i += end + 1
	}
	return false
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
