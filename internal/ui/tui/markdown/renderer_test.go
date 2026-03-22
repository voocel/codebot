package markdown

import (
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func TestRenderFinalAddsANSIStyling(t *testing.T) {
	r := NewRenderer(96)
	out := r.RenderFinal("# Title\n\n- item\n\n`这种` *这种* **这种**")
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI styling, got %q", out)
	}
	plain := stripANSI(out)
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
	if !strings.Contains(stripANSI(out), "fmt.Println(\"hi\")") {
		t.Fatalf("expected code content to stay visible, got %q", stripANSI(out))
	}
}

func TestRenderLiveStringReturnsRenderedOutput(t *testing.T) {
	r := NewRenderer(96)
	out := r.RenderLiveString("## Section\n\n- item")
	if out == "" {
		t.Fatal("expected rendered output")
	}
}
