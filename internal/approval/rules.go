package approval

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// Rule is a single permission rule parsed from settings.
type Rule struct {
	Raw     string // original string, e.g. "Bash(npm test *)"
	Kind    string // "Bash","Edit","Read","WebFetch","Subagent","tool"
	Pattern string // content inside parentheses, or bare tool name pattern
}

// RuleSet holds allow and deny rule lists.
type RuleSet struct {
	Allow []Rule
	Deny  []Rule
}

// ParseRuleSet parses raw allow/deny string arrays into a RuleSet.
// Returns nil if both arrays are empty.
func ParseRuleSet(allow, deny []string) (*RuleSet, error) {
	if len(allow) == 0 && len(deny) == 0 {
		return nil, nil
	}
	rs := &RuleSet{}
	for _, raw := range deny {
		r, err := ParseRule(raw)
		if err != nil {
			return nil, fmt.Errorf("deny rule %q: %w", raw, err)
		}
		rs.Deny = append(rs.Deny, r)
	}
	for _, raw := range allow {
		r, err := ParseRule(raw)
		if err != nil {
			return nil, fmt.Errorf("allow rule %q: %w", raw, err)
		}
		rs.Allow = append(rs.Allow, r)
	}
	return rs, nil
}

// ParseRule parses a single rule string like "Bash(npm test *)" or "mcp__ctx7__*".
func ParseRule(raw string) (Rule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Rule{}, fmt.Errorf("empty rule")
	}

	// Try "Kind(pattern)" format.
	if idx := strings.IndexByte(raw, '('); idx > 0 {
		if !strings.HasSuffix(raw, ")") {
			return Rule{}, fmt.Errorf("unclosed parenthesis in rule %q", raw)
		}
		kind := raw[:idx]
		pattern := raw[idx+1 : len(raw)-1]
		switch kind {
		case "Bash", "Edit", "Write", "Read", "WebFetch", "Subagent":
			return Rule{Raw: raw, Kind: kind, Pattern: pattern}, nil
		default:
			return Rule{}, fmt.Errorf("unknown rule kind %q", kind)
		}
	}

	// Bare tool name pattern (e.g. "mcp__context7__*").
	return Rule{Raw: raw, Kind: "tool", Pattern: raw}, nil
}

// Evaluate checks toolInfo against the rule set.
// Returns (action, true) if a rule matched, or ("", false) to fall through.
// Evaluation order: deny first, then allow.
func (rs *RuleSet) Evaluate(info toolInfo) (ruleAction, bool) {
	if rs == nil {
		return "", false
	}
	for _, r := range rs.Deny {
		if r.matches(info, true) {
			return ruleDeny, true
		}
	}
	for _, r := range rs.Allow {
		if r.matches(info, false) {
			return ruleAllow, true
		}
	}
	return "", false
}

func (r Rule) matches(info toolInfo, isDeny bool) bool {
	switch r.Kind {
	case "Bash":
		if info.tool != "bash" {
			return false
		}
		return matchBash(r.Pattern, info.summary, isDeny)
	case "Edit", "Write":
		if info.tool != "write" && info.tool != "edit" {
			return false
		}
		return matchPath(r.Pattern, info.summary, info.workspace)
	case "Read":
		switch info.tool {
		case "read", "glob", "grep", "ls":
		default:
			return false
		}
		return matchPath(r.Pattern, info.summary, info.workspace)
	case "WebFetch":
		if info.capability != CapNetwork {
			return false
		}
		host := hostFromSummary(info.summary)
		return matchHost(r.Pattern, host)
	case "Subagent":
		if info.tool != "subagent" {
			return false
		}
		return strings.EqualFold(r.Pattern, info.summary)
	case "tool":
		return matchToolName(r.Pattern, info.tool)
	}
	return false
}

// matchBash matches a bash command against a pattern.
// Pattern tokens are space-separated; trailing "*" matches all remaining tokens.
// For allow rules, compound commands (&&, ||, ;, |) never match.
// For deny rules, compound commands are split and any matching segment triggers denial.
func matchBash(pattern, command string, isDeny bool) bool {
	command = strings.TrimSpace(command)
	pattern = strings.TrimSpace(pattern)
	if command == "" || pattern == "" {
		return false
	}

	if hasShellOperator(command) {
		if !isDeny {
			return false // allow rules never match compound commands
		}
		for _, seg := range splitShellSegments(command) {
			seg = strings.TrimSpace(seg)
			if seg != "" && matchSimpleBash(pattern, seg) {
				return true
			}
		}
		return false
	}
	return matchSimpleBash(pattern, command)
}

func matchSimpleBash(pattern, command string) bool {
	pt := strings.Fields(pattern)
	ct := strings.Fields(command)
	for i, p := range pt {
		if p == "*" {
			return i < len(ct) // wildcard needs at least one remaining token
		}
		if i >= len(ct) || p != ct[i] {
			return false
		}
	}
	return len(pt) == len(ct)
}

// hasShellOperator reports whether cmd contains unquoted shell operators.
func hasShellOperator(cmd string) bool {
	return len(splitShellSegments(cmd)) > 1
}

// splitShellSegments splits a command by unquoted shell operators (&&, ||, ;).
// Quoted content (single quotes, double quotes, backslash escapes) is preserved.
func splitShellSegments(cmd string) []string {
	var (
		parts    []string
		buf      strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)

	flush := func() {
		part := strings.TrimSpace(buf.String())
		if part != "" {
			parts = append(parts, part)
		}
		buf.Reset()
	}

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]

		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			buf.WriteByte(ch)
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			buf.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			buf.WriteByte(ch)
			continue
		}

		if !inSingle && !inDouble {
			if ch == ';' {
				flush()
				continue
			}
			if i+1 < len(cmd) && ((ch == '&' && cmd[i+1] == '&') || (ch == '|' && cmd[i+1] == '|')) {
				flush()
				i++ // skip second character of && or ||
				continue
			}
			if ch == '|' {
				flush()
				continue
			}
		}

		buf.WriteByte(ch)
	}
	flush()

	return parts
}

// matchPath matches a file path against a glob pattern.
// Tries both the absolute path and the workspace-relative path.
func matchPath(pattern, path, workspace string) bool {
	if matchGlob(pattern, path) {
		return true
	}
	// Try relative to workspace.
	if workspace != "" && filepath.IsAbs(path) {
		rel, err := filepath.Rel(workspace, path)
		if err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return matchGlob(pattern, rel)
		}
	}
	return false
}

// matchGlob matches a file path against a glob pattern with ** support.
func matchGlob(pattern, path string) bool {
	pattern = strings.TrimSpace(pattern)
	path = strings.TrimSpace(path)
	if pattern == "" || path == "" {
		return false
	}

	// Handle ** by splitting into segments.
	if strings.Contains(pattern, "**") {
		return matchDoubleStarGlob(pattern, path)
	}

	matched, _ := filepath.Match(pattern, path)
	if matched {
		return true
	}
	// Also try matching against the base name for relative patterns.
	matched, _ = filepath.Match(pattern, filepath.Base(path))
	return matched
}

// matchDoubleStarGlob handles ** in glob patterns.
// "src/**" matches anything under src/. "src/**/*.go" matches .go files at any depth.
func matchDoubleStarGlob(pattern, path string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	prefix := strings.TrimRight(parts[0], string(filepath.Separator))
	suffix := ""
	if len(parts) > 1 {
		suffix = strings.TrimLeft(parts[1], string(filepath.Separator))
	}

	// Path must start with prefix.
	if prefix != "" {
		cleanPath := filepath.Clean(path)
		cleanPrefix := filepath.Clean(prefix)
		if !strings.HasPrefix(cleanPath, cleanPrefix+string(filepath.Separator)) && cleanPath != cleanPrefix {
			return false
		}
	}

	// If no suffix after **, match everything under prefix.
	if suffix == "" {
		return true
	}

	// Suffix must match the tail of the path.
	matched, _ := filepath.Match(suffix, filepath.Base(path))
	return matched
}

// matchHost matches a hostname against a pattern like "*.github.com" or "example.com".
func matchHost(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" || host == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".github.com"
		return host == pattern[2:] || strings.HasSuffix(host, suffix)
	}
	return pattern == host
}

// matchToolName matches a tool name with optional trailing wildcard.
func matchToolName(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	name = strings.TrimSpace(name)
	if pattern == "" || name == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return pattern == name
}

// hostFromSummary extracts hostname from a URL-like summary string.
func hostFromSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if parsed, err := url.Parse(summary); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	// For web_search, summary is the query text — no host to extract.
	return strings.ToLower(summary)
}
