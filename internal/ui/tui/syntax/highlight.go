// Package syntax colors code with chroma for the terminal. It emits only
// foreground SGR codes and foreground-only resets, never a full reset, so the
// code keeps a background an enclosing style paints, as a diff line's.
package syntax

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// The base styles paint strings green, which blends into the added-line
// background of a diff, so strings get another hue. tango also paints
// structural tokens bold black; lightExtras retones them.
var lightExtras = chroma.StyleEntries{
	chroma.Punctuation:   "nobold #57606A",
	chroma.NameFunction:  "#6F42C1",
	chroma.NameClass:     "#6F42C1",
	chroma.NameDecorator: "#6F42C1",
	chroma.LiteralNumber: "#0550AE",
	chroma.OperatorWord:  "nobold #0550AE",
}

var (
	darkStyle  = mustStyle("nord", "#D08770", nil)
	lightStyle = mustStyle("tango", "#A04100", lightExtras)
)

func mustStyle(base, stringHex string, extras chroma.StyleEntries) *chroma.Style {
	b := styles.Get(base).Builder()
	for _, tt := range []chroma.TokenType{chroma.LiteralString, chroma.LiteralStringDouble, chroma.LiteralStringSingle, chroma.LiteralStringBacktick, chroma.LiteralStringChar} {
		b.Add(tt, stringHex)
	}
	for tt, entry := range extras {
		b.Add(tt, entry)
	}
	s, err := b.Build()
	if err != nil {
		panic("syntax: style " + base + ": " + err.Error())
	}
	return s
}

// File colors code from the file at path, chosen by its name; code it has no
// lexer for comes back as it is.
func File(code, path string) string { return highlight(code, lexers.Match(path)) }

// Lang colors code in the language a markdown fence names.
func Lang(code, lang string) string {
	if lang == "" {
		return code
	}
	return highlight(code, lexers.Get(lang))
}

func highlight(code string, lexer chroma.Lexer) string {
	if code == "" || lexer == nil {
		return code
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return code
	}
	style := lightStyle
	if theme.Dark {
		style = darkStyle
	}
	var out strings.Builder
	out.Grow(len(code) + 64)
	for tok := it(); tok != chroma.EOF; tok = it() {
		writeToken(&out, style.Get(tok.Type), tok.Value)
	}
	return out.String()
}

// writeToken writes text in entry's foreground, bold and italic, and resets
// just those.
func writeToken(out *strings.Builder, entry chroma.StyleEntry, text string) {
	if text == "" {
		return
	}
	var on []string
	if entry.Bold == chroma.Yes {
		on = append(on, "1")
	}
	if entry.Italic == chroma.Yes {
		on = append(on, "3")
	}
	if entry.Colour.IsSet() {
		on = append(on, fmt.Sprintf("38;2;%d;%d;%d", entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue()))
	}
	if len(on) == 0 {
		out.WriteString(text)
		return
	}
	// A token may span lines; each line gets its own codes so lines stand
	// alone once split.
	open := "\x1b[" + strings.Join(on, ";") + "m"
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		if line != "" {
			out.WriteString(open + line + "\x1b[22;23;39m")
		}
	}
}
