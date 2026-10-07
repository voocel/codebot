package hooks

import (
	"fmt"
	"regexp"
	"strings"
)

type matcher interface {
	Match(toolName string) bool
}

type anyMatcher struct{}

func (anyMatcher) Match(string) bool { return true }

type exactMatcher struct{ name string }

func (m exactMatcher) Match(toolName string) bool {
	return strings.EqualFold(m.name, toolName)
}

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) Match(toolName string) bool {
	return m.re.MatchString(toolName)
}

// parseMatcher accepts:
//
//	""           → any tool
//	"/pattern/"  → regexp
//	"/pattern/i" → case-insensitive regexp
//	other        → exact name, case-insensitive
func parseMatcher(s string) (matcher, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return anyMatcher{}, nil
	}

	if strings.HasPrefix(s, "/") {
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
