// Package printable makes file text safe to show: a terminal acts on control
// and bidirectional characters instead of showing them, so a file could
// erase or reorder what the user reads.
package printable

import (
	"strconv"
	"strings"
	"unicode"
)

// Escape returns s unchanged if every character is printable and s does not
// start with a quote; otherwise it returns s Go-quoted. Distinct inputs never
// look the same.
func Escape(s string) string {
	if !strings.HasPrefix(s, `"`) && !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return s
	}
	return strconv.Quote(s)
}

// Lines returns s with each character a terminal would act on, not show,
// written out as its escape, such as \x1b: lines and tabs stay.
func Lines(s string) string {
	if !strings.ContainsFunc(s, hidden) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if hidden(r) {
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hidden(r rune) bool { return r != '\n' && r != '\t' && !unicode.IsPrint(r) }
