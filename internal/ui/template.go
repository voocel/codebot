package ui

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseArgs splits a command argument string respecting double and single quotes.
// Empty quoted strings are skipped.
func ParseArgs(s string) []string {
	var args []string
	var buf strings.Builder
	var inQuote rune

	for _, r := range s {
		switch {
		case inQuote != 0:
			if r == inQuote {
				if buf.Len() > 0 {
					args = append(args, buf.String())
					buf.Reset()
				}
				inQuote = 0
			} else {
				buf.WriteRune(r)
			}
		case r == '"' || r == '\'':
			if buf.Len() > 0 {
				args = append(args, buf.String())
				buf.Reset()
			}
			inQuote = r
		case r == ' ' || r == '\t':
			if buf.Len() > 0 {
				args = append(args, buf.String())
				buf.Reset()
			}
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		args = append(args, buf.String())
	}
	return args
}

// rePositional matches $1, $2, ..., $99.
var rePositional = regexp.MustCompile(`\$(\d{1,2})`)

// reSlice matches ${@:N} and ${@:N:L}.
var reSlice = regexp.MustCompile(`\$\{@:(\d+)(?::(\d+))?\}`)

// Expand substitutes placeholder patterns in template with the given args.
//
// Supported patterns:
//   - $1, $2, ... $N  — positional args (1-based, out-of-range → "")
//   - ${@:N}          — all args from index N onward (1-based)
//   - ${@:N:L}        — L args starting at index N (1-based)
//   - $@ / $ARGUMENTS — all args joined by space
//
// Substitution is single-pass per pattern class (no recursive expansion).
func Expand(template string, args []string) string {
	allArgs := strings.Join(args, " ")

	// 1. Positional: $1, $2, ...
	result := rePositional.ReplaceAllStringFunc(template, func(m string) string {
		n, _ := strconv.Atoi(m[1:])
		if n < 1 || n > len(args) {
			return ""
		}
		return args[n-1]
	})

	// 2. Slice: ${@:N} and ${@:N:L}
	result = reSlice.ReplaceAllStringFunc(result, func(m string) string {
		sub := reSlice.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		start, _ := strconv.Atoi(sub[1])
		if start < 1 {
			start = 1
		}
		idx := start - 1
		if idx >= len(args) {
			return ""
		}
		if sub[2] == "" {
			return strings.Join(args[idx:], " ")
		}
		length, _ := strconv.Atoi(sub[2])
		end := idx + length
		if end > len(args) {
			end = len(args)
		}
		return strings.Join(args[idx:end], " ")
	})

	// 3. $ARGUMENTS and $@ — all args
	result = strings.ReplaceAll(result, "$ARGUMENTS", allArgs)
	result = strings.ReplaceAll(result, "$@", allArgs)

	return result
}
