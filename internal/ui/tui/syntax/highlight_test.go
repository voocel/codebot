package syntax

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Full reset (ESC[0m) anywhere in output would clear the outer bg span
// mid-row. This is the load-bearing invariant of the whole package.
func TestHighlightNeverEmitsFullReset(t *testing.T) {
	cases := []struct {
		name string
		path string
		code string
	}{
		{"go", "main.go", `package main

func main() {
	x := "hello" // greeting
	_ = x
}`},
		{"json", "data.json", `{"a": 1, "b": "two"}`},
		{"unknown extension", "weird.xyz", `anything goes here`},
		{"plain text fallback", "", `no path no lexer`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Highlight(tc.code, tc.path)
			if strings.Contains(out, "\x1b[0m") {
				t.Fatalf("output contains full reset (ESC[0m), would break outer bg span; out=%q", out)
			}
		})
	}
}

func TestHighlightPreservesText(t *testing.T) {
	cases := []struct {
		name string
		path string
		code string
	}{
		{"go", "main.go", `func f(x int) int { return x + 1 }`},
		{"empty input passes through", "main.go", ``},
		{"unicode", "x.go", `s := "héllo 你好"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Highlight(tc.code, tc.path)
			if got := stripANSI(out); got != tc.code {
				t.Fatalf("stripped output = %q, want %q", got, tc.code)
			}
		})
	}
}

// Asserts against Nord/Tango's *Keyword* colours, not our LiteralString
// overrides — picking a token we don't override means the test still
// catches a regression where the dark/light switch breaks and both
// branches collapse onto the same base.
func TestHighlightSwitchesPaletteByBackground(t *testing.T) {
	code := "func main() {}"
	path := "main.go"

	prev := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(prev) })

	lipgloss.SetHasDarkBackground(true)
	dark := Highlight(code, path)

	lipgloss.SetHasDarkBackground(false)
	light := Highlight(code, path)

	if dark == light {
		t.Fatal("dark and light palettes produced identical output — background detection is not driving style selection")
	}

	// Nord Keyword "func" = #81a1c1 (129,161,193).
	// Tango Keyword "func" = #204a87 (32,74,135).
	if !strings.Contains(dark, "38;2;129;161;193") {
		t.Fatalf("dark output missing Nord Keyword colour (129;161;193): %q", dark)
	}
	if !strings.Contains(light, "38;2;32;74;135") {
		t.Fatalf("light output missing Tango Keyword colour (32;74;135): %q", light)
	}
}

// Guard against tango's bold-black structural tokens leaking through.
func TestHighlightLightMutesBoldBlackPunctuation(t *testing.T) {
	prev := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(prev) })
	lipgloss.SetHasDarkBackground(false)

	out := Highlight("func main() {}", "main.go")

	// #57606A = (87,96,106) — our muted Punctuation colour.
	if !strings.Contains(out, "38;2;87;96;106") {
		t.Fatalf("light Punctuation override (#57606A) missing from output: %q", out)
	}
	// Tango's default `bold + #000000` for Punctuation produces
	// `\x1b[1;38;2;0;0;0m`. Our writer always emits bold first, so
	// that exact prefix is the regression marker.
	if strings.Contains(out, "\x1b[1;38;2;0;0;0m") {
		t.Fatalf("light output still uses bold-black for some token, override didn't reach all bold-black entries: %q", out)
	}
	// #6F42C1 = (111,66,193) — purple NameFunction.
	if !strings.Contains(out, "38;2;111;66;193") {
		t.Fatalf("light NameFunction override (#6F42C1) missing: %q", out)
	}
}

// Both bases ship green LiteralString that clashes with DiffAddBg —
// override must reach the wire on dark and light.
func TestHighlightStringOverrideOnBothPalettes(t *testing.T) {
	code := `s := "hello"`
	path := "main.go"

	prev := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(prev) })

	lipgloss.SetHasDarkBackground(true)
	dark := Highlight(code, path)
	// stringOverrideDark = #d08770 (208,135,112)
	if !strings.Contains(dark, "38;2;208;135;112") {
		t.Fatalf("dark string override (208;135;112) missing: %q", dark)
	}
	// Sanity: original Nord green must NOT appear for the string token.
	if strings.Contains(dark, "38;2;163;190;140") {
		t.Fatalf("dark output still uses Nord's default green LiteralString (163;190;140): %q", dark)
	}

	lipgloss.SetHasDarkBackground(false)
	light := Highlight(code, path)
	// stringOverrideLight = #A04100 (160,65,0)
	if !strings.Contains(light, "38;2;160;65;0") {
		t.Fatalf("light string override (160;65;0) missing: %q", light)
	}
	// Sanity: Tango's default green must NOT appear.
	if strings.Contains(light, "38;2;78;154;6") {
		t.Fatalf("light output still uses Tango's default green LiteralString (78;154;6): %q", light)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
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
