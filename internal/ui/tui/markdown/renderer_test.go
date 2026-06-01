package markdown

import (
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSITest(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func TestRenderFinalInlineStylesMatchClaudeHierarchy(t *testing.T) {
	r := NewRenderer(96)
	out := r.RenderFinal("*italic* **bold** `code`")
	if !strings.Contains(out, "\x1b[3mitalic\x1b[0m") {
		t.Fatalf("expected italic markdown to render as italic, got %q", out)
	}
	if !strings.Contains(out, "\x1b[1mbold\x1b[0m") {
		t.Fatalf("expected bold markdown to render as bold, got %q", out)
	}
	if !strings.Contains(out, "\x1b[38;5;") {
		t.Fatalf("expected inline code to render with semantic color, got %q", out)
	}
	if strings.Contains(out, "\x1b[1mcode\x1b[0m") {
		t.Fatalf("expected inline code not to share bold styling, got %q", out)
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

func TestRenderFinalLinkUsesColorWithoutUnderline(t *testing.T) {
	r := NewRenderer(96)
	out := r.RenderFinal("[docs](https://example.com)")
	if !strings.Contains(out, "\x1b[38;5;") {
		t.Fatalf("expected link to use restrained color, got %q", out)
	}
	if strings.Contains(out, "\x1b[4m") {
		t.Fatalf("expected link not to use underline, got %q", out)
	}
	if stripANSITest(out) != "docs (https://example.com)" {
		t.Fatalf("expected link text and url to stay visible, got %q", stripANSITest(out))
	}
}

func TestRenderFinalHorizontalRuleAsLiteralDashes(t *testing.T) {
	r := NewRenderer(40)
	out := stripANSITest(r.RenderFinal("---"))
	if out != "---" {
		t.Fatalf("expected hr to render as literal dashes, got %q", out)
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
	if strings.Contains(rendered, "\x1b[38;5;") {
		t.Fatalf("expected restrained table rendering without colored separators, got %q", rendered)
	}
}
