package commands

import (
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

// ParseInvocation parses a slash-command input like "/foo bar baz" into an Invocation.
// Returns ok=false for non-command input (empty, no leading "/", or only "/").
func ParseInvocation(input string) (Invocation, bool) {
	input = strings.TrimSpace(input)
	if input == "" || !strings.HasPrefix(input, "/") {
		return Invocation{}, false
	}

	body := strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if body == "" {
		return Invocation{}, false
	}

	name := body
	rawArgs := ""
	if idx := strings.IndexAny(body, " \t"); idx >= 0 {
		name = body[:idx]
		rawArgs = strings.TrimSpace(body[idx+1:])
	}

	return Invocation{
		Input:   input,
		Name:    strings.ToLower(name),
		RawArgs: rawArgs,
		Args:    ParseArgs(rawArgs),
	}, true
}
