// Package printable shows text from files as what it says: a terminal acts
// on control and bidirectional characters rather than showing them, so a
// file could erase or reorder what the user reads.
package printable

import (
	"strconv"
	"strings"
	"unicode"
)

// Escape returns s with each character a terminal would act on rather than
// show written as its Go escape.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		q := strconv.QuoteRune(r)
		b.WriteString(q[1 : len(q)-1])
	}
	return b.String()
}
