// Package syntax wraps chroma to emit ANSI-colored code that nests safely
// inside a lipgloss background-color span. The standard chroma TTY
// formatter terminates every token with ESC[0m (full SGR reset), which
// would clear the diff line's bg tint mid-row. We emit only foreground
// SGR plus a foreground-only reset (39) so an outer Background() stays
// intact.
package syntax

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// Both nord and tango ship a green LiteralString that blends into DiffAddBg —
// override to a non-clashing hue. tango additionally paints structural tokens
// as bold pure black (Punctuation, NameFunction), which reads as ink-on-paper
// rather than syntax colour; lightExtras retones those to GitHub Primer.
const (
	stringOverrideDark  = "#d08770"
	stringOverrideLight = "#A04100"
)

var lightExtras = chroma.StyleEntries{
	chroma.Punctuation:   "nobold #57606A",
	chroma.NameFunction:  "#6F42C1",
	chroma.NameClass:     "#6F42C1",
	chroma.NameDecorator: "#6F42C1",
	chroma.LiteralNumber: "#0550AE",
	chroma.OperatorWord:  "nobold #0550AE",
}

var (
	darkDiffStyle  = mustOverride("nord", stringOverrideDark, nil)
	lightDiffStyle = mustOverride("tango", stringOverrideLight, lightExtras)
)

func mustOverride(baseName, stringHex string, extras chroma.StyleEntries) *chroma.Style {
	base := styles.Get(baseName)
	if base == nil || base.Name == "swapoff" {
		panic("syntax: chroma style not found: " + baseName)
	}
	builder := base.Builder().
		Add(chroma.LiteralString, stringHex).
		Add(chroma.LiteralStringDouble, stringHex).
		Add(chroma.LiteralStringSingle, stringHex).
		Add(chroma.LiteralStringBacktick, stringHex).
		Add(chroma.LiteralStringChar, stringHex)
	for tt, entry := range extras {
		builder = builder.Add(tt, entry)
	}
	out, err := builder.Build()
	if err != nil {
		panic("syntax: failed to override style " + baseName + ": " + err.Error())
	}
	return out
}

func activeStyle() *chroma.Style {
	if lipgloss.HasDarkBackground() {
		return darkDiffStyle
	}
	return lightDiffStyle
}

// Highlight returns code annotated with ANSI fg SGR escapes inferred from
// filePath. Output ends with "\x1b[39m" and contains no full resets, so
// wrapping it in a background span produces a continuous colored band.
// Unrecognised paths or lex errors return the input verbatim.
func Highlight(code, filePath string) string {
	if code == "" {
		return code
	}

	lexer := lexers.Match(filePath)
	if lexer == nil {
		return code
	}
	lexer = chroma.Coalesce(lexer)

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}

	var out strings.Builder
	out.Grow(len(code) + 32)
	style := activeStyle()
	for token := iterator(); token != chroma.EOF; token = iterator() {
		entry := style.Get(token.Type)
		writeToken(&out, entry, token.Value)
	}
	out.WriteString("\x1b[39m")
	return out.String()
}

// writeToken emits fg + bold/italic for one token, then resets via 22;23;39
// (never 0 — a full reset would cancel the caller's background).
func writeToken(out *strings.Builder, entry chroma.StyleEntry, text string) {
	if text == "" {
		return
	}
	hasFg := entry.Colour.IsSet()
	if hasFg || entry.Bold == chroma.Yes || entry.Italic == chroma.Yes {
		out.WriteString("\x1b[")
		first := true
		if entry.Bold == chroma.Yes {
			out.WriteString("1")
			first = false
		}
		if entry.Italic == chroma.Yes {
			if !first {
				out.WriteString(";")
			}
			out.WriteString("3")
			first = false
		}
		if hasFg {
			if !first {
				out.WriteString(";")
			}
			fmt.Fprintf(out, "38;2;%d;%d;%d", entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue())
		}
		out.WriteString("m")
	}
	out.WriteString(text)
	if entry.Bold == chroma.Yes || entry.Italic == chroma.Yes {
		out.WriteString("\x1b[22;23")
		if hasFg {
			out.WriteString(";39")
		}
		out.WriteString("m")
	} else if hasFg {
		out.WriteString("\x1b[39m")
	}
}
