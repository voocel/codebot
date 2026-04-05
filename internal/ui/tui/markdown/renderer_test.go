package markdown

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSITest(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func TestRenderFinalAddsANSIStyling(t *testing.T) {
	r := NewRenderer(96)
	out := r.RenderFinal("# Title\n\n- item\n\n`这种` *这种* **这种**")
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI styling, got %q", out)
	}
	plain := stripANSITest(out)
	for _, want := range []string{"Title", "item", "这种"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered markdown to keep content %q, got %q", want, plain)
		}
	}
}

func TestRenderFinalFencedCodeBlockDoesNotPanic(t *testing.T) {
	r := NewRenderer(96)
	out := r.RenderFinal("```go\nfmt.Println(\"hi\")\n```")
	if out == "" {
		t.Fatal("expected fenced code block to render")
	}
	if !strings.Contains(stripANSITest(out), "fmt.Println(\"hi\")") {
		t.Fatalf("expected code content to stay visible, got %q", stripANSITest(out))
	}
}

func TestRenderFinalHeadingKeepsStyleAfterInlineLink(t *testing.T) {
	r := NewRenderer(96)
	out := r.RenderFinal("### 与 [novelist/](https://example.com) 的关系")
	plain := stripANSITest(out)
	if !strings.Contains(plain, "与 novelist/ (https://example.com) 的关系") {
		t.Fatalf("expected heading text to stay intact, got %q", plain)
	}
	if count := strings.Count(out, headingPrefix(3)); count < 2 {
		t.Fatalf("expected heading style to resume after inline content, got %q", out)
	}
}

func TestRenderFinalHorizontalRuleExpandsSeparator(t *testing.T) {
	r := NewRenderer(40)
	out := stripANSITest(r.RenderFinal("---"))
	if got := len([]rune(out)); got <= 10 {
		t.Fatalf("expected expanded separator, got %q", out)
	}
	if strings.Trim(out, "─") != "" {
		t.Fatalf("expected separator to contain only rule characters, got %q", out)
	}
}

func TestRenderFinalFormatsMarkdownTable(t *testing.T) {
	r := NewRenderer(96)
	rendered := r.RenderFinal(strings.Join([]string{
		"| Name | Value |",
		"| ---- | ----- |",
		"| A | 1 |",
		"| Bee | 23 |",
	}, "\n"))
	out := stripANSITest(rendered)
	for _, want := range []string{
		"┌",
		"│ Name │ Value │",
		"├",
		"│ A",
		"├",
		"│ Bee",
		"└",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected formatted table row %q, got %q", want, out)
		}
	}
	if !strings.Contains(rendered, renderSeparator("│")) {
		t.Fatalf("expected vertical borders to use separator styling, got %q", rendered)
	}
}

func TestRenderFinalFormatsMarkdownTableWithCJK(t *testing.T) {
	r := NewRenderer(96)
	out := stripANSITest(r.RenderFinal(strings.Join([]string{
		"| 文件 | 方向 |",
		"| ---- | ---- |",
		"| internal/ui/cmd_btw.go | UI 命令相关 |",
		"| internal/ui/tui/render.go | 渲染逻辑 |",
	}, "\n")))
	lines := strings.Split(out, "\n")
	if len(lines) != 7 {
		t.Fatalf("expected 7 rendered lines, got %d: %q", len(lines), out)
	}
	wantWidth := lipgloss.Width(lines[0])
	for i, line := range lines {
		if got := lipgloss.Width(line); got != wantWidth {
			t.Fatalf("expected line %d width %d, got %d: %q", i, wantWidth, got, line)
		}
	}
}
