package hooks

import (
	"fmt"
	"regexp"
	"strings"
)

// matcher decides whether a hook should run for a given tool name.
type matcher interface {
	Match(toolName string) bool
}

// anyMatcher matches every tool (used when no matcher is configured).
type anyMatcher struct{}

func (anyMatcher) Match(string) bool { return true }

// exactMatcher matches a single tool name (case-insensitive).
type exactMatcher struct{ name string }

func (m exactMatcher) Match(toolName string) bool {
	return strings.EqualFold(m.name, toolName)
}

// regexMatcher matches tool names against a compiled regular expression.
type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) Match(toolName string) bool {
	return m.re.MatchString(toolName)
}

// parseMatcher parses a matcher string into a matcher.
//
//	""              → anyMatcher (matches all tools)
//	"/pattern/"     → regexMatcher
//	"/pattern/i"    → regexMatcher (case-insensitive)
//	other           → exactMatcher (case-insensitive)
func parseMatcher(s string) (matcher, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return anyMatcher{}, nil
	}

	// Regex: /pattern/ or /pattern/i
	if strings.HasPrefix(s, "/") {
		// Find closing slash (may be followed by flags).
		end := strings.LastIndex(s[1:], "/")
		if end < 0 {
			return exactMatcher{name: s}, nil // no closing slash, treat as exact
		}
		end++ // adjust for s[1:] offset
		pattern := s[1:end]
		flags := s[end+1:]

		if strings.Contains(flags, "i") {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid matcher regex %q: %w", s, err)
		}
		return regexMatcher{re: re}, nil
	}

	return exactMatcher{name: s}, nil
}
