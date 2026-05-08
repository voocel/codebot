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

	out := RenderEditResult(payload, "", 40)
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

// Context lines must carry chroma fg but never diff bg.
func TestRenderEditResultContextHasHighlightNoBg(t *testing.T) {
	pinTrueColor(t)
	diff := " 1 package main\n" +
		"-2 old\n" +
		"+2 new\n" +
		" 3 import \"fmt\"\n"
	payload, _ := json.Marshal(map[string]any{"diff": diff})

	out := RenderEditResult(payload, "main.go", 50)
	lines := strings.Split(out, "\n")
	if len(lines) < 5 {
		t.Fatalf("expected stats + 4 rows, got %d:\n%s", len(lines), out)
	}

	// Rows 1 and 4 are context (` 1 package main`, ` 3 import "fmt"`).
	for _, idx := range []int{1, 4} {
		row := lines[idx]
		if !strings.Contains(row, "\x1b[38;2;") {
			t.Fatalf("context row %d should carry chroma fg ANSI: %q", idx, row)
		}
		if hasBgEscape(row) {
			t.Fatalf("context row %d must not carry diff bg ANSI: %q", idx, row)
		}
	}
}

// Highlighted rows must still pad to width — guards ANSI-nesting bugs
// where a chroma reset would expose a gap between code and padding.
func TestRenderEditResultHighlightedKeepsBgIntact(t *testing.T) {
	pinTrueColor(t)
	diff := strings.Join([]string{
		"-1 func old() {}",
		"+1 func renamed() {}",
	}, "\n")
	payload, _ := json.Marshal(map[string]any{"diff": diff})

	out := RenderEditResult(payload, "main.go", 60)
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected stats + 2 diff lines, got %d:\n%s", len(lines), out)
	}

	// Last two lines are the diff rows. Each must pad to width AND contain
	// at least one fg ANSI from chroma (proving highlighting actually ran
	// on a recognised lexer).
	for _, idx := range []int{1, 2} {
		row := lines[idx]
		if got := lipgloss.Width(row); got != 60 {
			t.Fatalf("row %d visible width = %d, want 60; row=%q", idx, got, row)
		}
		if !strings.Contains(row, "\x1b[38;2;") {
			t.Fatalf("row %d missing chroma fg ANSI (highlighting didn't run): %q", idx, row)
		}
	}
}

func TestRenderDiffLinePadsToWidth(t *testing.T) {
	pinTrueColor(t)
	out := renderDiffLine("+5 hi", DiffAddGutterStyle, DiffAddBodyStyle, "", 30)
	if got := lipgloss.Width(out); got != 30 {
		t.Fatalf("padded width = %d, want 30; out=%q", got, out)
	}
}

// Highlighter must run on every wrap segment, not just the first.
func TestRenderDiffLineWrapPreservesHighlightOnContinuation(t *testing.T) {
	pinTrueColor(t)
	long := "+5 " + strings.Repeat("var x int = 1; ", 20) // long Go statement
	out := renderDiffLine(long, DiffAddGutterStyle, DiffAddBodyStyle, "main.go", 30)
	rows := strings.Split(out, "\n")
	if len(rows) < 2 {
		t.Fatalf("expected wrap to produce >=2 rows, got %d", len(rows))
	}
	for i, row := range rows {
		if !strings.Contains(row, "\x1b[38;2;") {
			t.Fatalf("row %d missing chroma fg ANSI — wrap dropped highlighting: %q", i, row)
		}
	}
}

// Long bodies must self-wrap so the bg band stays continuous; continuation
// rows blank the line-number column but keep the sigil.
func TestRenderDiffLineWrapsLongLine(t *testing.T) {
	pinTrueColor(t)
	long := "+5 " + strings.Repeat("x", 100)
	out := renderDiffLine(long, DiffAddGutterStyle, DiffAddBodyStyle, "", 20)
	rows := strings.Split(out, "\n")
	if len(rows) < 2 {
		t.Fatalf("expected long line to wrap into multiple rows, got %d row(s):\n%s", len(rows), out)
	}
	for i, row := range rows {
		if got := lipgloss.Width(row); got != 20 {
			t.Fatalf("row %d width = %d, want 20; row=%q", i, got, row)
		}
	}

	visibleRows := make([]string, len(rows))
	for i, row := range rows {
		visibleRows[i] = stripANSIInTest(row)
	}
	for i := 1; i < len(visibleRows); i++ {
		row := visibleRows[i]
		if len(row) < 2 || row[0] != '+' {
			t.Fatalf("continuation row %d should start with '+', got %q", i, row)
		}
		if row[1] != ' ' {
			t.Fatalf("continuation row %d should blank the line-number column (space at idx 1), got %q", i, row)
		}
	}
}

func stripANSIInTest(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := strings.IndexByte(s[i:], 'm')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
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
