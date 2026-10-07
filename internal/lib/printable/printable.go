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
