package markdown

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const sample = "# Title\n\nSome **bold** and *italic* text with `code` and a [link](https://example.com) that goes on long enough to wrap around the width.\nA soft break stays.\n\n- one\n- two\n  - nested item that is also quite long and should wrap nicely under its marker\n1. first\n2. second\n\n> quoted text\n\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n\n| a | b |\n|---|--:|\n| 1 | 22 |\n\n---\n\n中文段落没有空格也应该在宽度处正确换行，不会超出边界。"

func TestRenderFitsWidth(t *testing.T) {
	for _, width := range []int{8, 20, 40, 80} {
		for i, line := range Render(sample, width) {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("width %d: line %d is %d wide: %q", width, i, w, ansi.Strip(line))
			}
		}
	}
}

func TestRenderContent(t *testing.T) {
	plain := ansi.Strip(strings.Join(Render(sample, 40), "\n"))
	for _, want := range []string{"Title", "bold", "link (https://example.com)", "A soft break stays.", "• one", "◦ nested", "1. first", "▎ quoted text", "│ func main() {", "│     fmt.Println(\"hi\")", "│ 1 │ 22 │"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in\n%s", want, plain)
		}
	}
	if testing.Verbose() {
		t.Log("\n" + strings.Join(Render(sample, 40), "\n"))
	}
}

func TestRenderTableTooWideGoesVertical(t *testing.T) {
	src := "| name | description |\n|---|---|\n| x | a long description that cannot fit |\n"
	plain := ansi.Strip(strings.Join(Render(src, 20), "\n"))
	if !strings.Contains(plain, "name: x") {
		t.Errorf("want a vertical table, got\n%s", plain)
	}
}

func TestWrapKeepsSpacing(t *testing.T) {
	got := Wrap("a  b\n\n  c", lipgloss.NewStyle(), 10)
	want := []string{"a  b", "", "  c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Wrap = %q, want %q", got, want)
	}
}
