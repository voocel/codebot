// Package printable shows text from files as what it says: a terminal acts
// on control and bidirectional characters rather than showing them, so a
// file could erase or reorder what the user reads.
package printable

import (
	"strconv"
	"strings"
	"unicode"
)

// Escape returns s as a terminal is to show it: as it is where it shows
// every character it holds and starts with no quote, else Go-quoted, what a
// terminal would act on written as its escape. No two strings show alike.
func Escape(s string) string {
	if !strings.HasPrefix(s, `"`) && !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return s
	}
	return strconv.Quote(s)
}
