package permission

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

type Rule struct {
	Raw     string
	Kind    string
	Pattern string
}

type RuleSet struct {
	Allow []Rule
	Deny  []Rule
}

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

func ParseRule(raw string) (Rule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Rule{}, fmt.Errorf("empty rule")
	}
	if idx := strings.IndexByte(raw, '('); idx > 0 {
		if !strings.HasSuffix(raw, ")") {
			return Rule{}, fmt.Errorf("unclosed parenthesis in rule %q", raw)
		}
		kind := raw[:idx]
		pattern := raw[idx+1 : len(raw)-1]
		switch kind {
		case "Bash", "Edit", "Write", "Read", "WebFetch":
			return Rule{Raw: raw, Kind: kind, Pattern: pattern}, nil
		default:
			return Rule{}, fmt.Errorf("unknown rule kind %q", kind)
		}
	}
	return Rule{Raw: raw, Kind: "tool", Pattern: raw}, nil
}

// evaluate lets a deny rule win over an allow rule.
func (rs *RuleSet) evaluate(info toolInfo) ruleAction {
	if rs == nil {
		return ""
	}
	for _, r := range rs.Deny {
		if r.matches(info, true) {
			return ruleDeny
		}
	}
	for _, r := range rs.Allow {
		if r.matches(info, false) {
			return ruleAllow
		}
	}
	return ""
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
		return matchPath(r.Pattern, info.summary, info.workspace, info.roots)
	case "Read":
		switch info.tool {
		case "read", "glob", "grep", "ls":
		default:
			return false
		}
		return matchPath(r.Pattern, info.summary, info.workspace, info.roots)
	case "WebFetch":
		if info.tool != "web_fetch" {
			return false
		}
		return matchHost(r.Pattern, hostOf(info.summary))
	case "tool":
		return matchToolName(r.Pattern, info.tool)
	default:
		return false
	}
}

// matchBash matches a command against the words of pattern. An allow rule
// covers a single command that hides nothing; a deny rule any command the
// line may run, however nested or wrapped, by its name or base name, as
// /bin/rm is rm.
func matchBash(pattern, line string, isDeny bool) bool {
	words := strings.Fields(pattern)
	if len(words) == 0 {
		return false
	}
	if isDeny {
		return eachCommand(line, func(args []string) bool { return matchWords(words, args, true) })
	}
	s := parseScript(line)
	return !s.opaque && len(s.cmds) == 1 && matchWords(words, s.cmds[0].args, false)
}

// matchWords reports whether args begin with pattern, where "*" stands for
// the rest of them, if any, as xargs rm has none; with base, the command
// also matches by base name.
func matchWords(pattern, args []string, base bool) bool {
	for i, p := range pattern {
		switch {
		case p == "*":
			return true
		case i >= len(args):
			return false
		case args[i] != p && !(base && i == 0 && path.Base(args[i]) == p):
			return false
		}
	}
	return true
}

func matchPath(pattern, path, workspace string, roots []string) bool {
	if matchGlob(pattern, path) {
		return true
	}
	if len(roots) == 0 && workspace != "" {
		roots = []string{workspace}
	}
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(path) {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			if matchGlob(pattern, rel) {
				return true
			}
		}
	}
	return false
}

func matchGlob(pattern, path string) bool {
	pattern = strings.TrimSpace(pattern)
	path = strings.TrimSpace(path)
	if pattern == "" || path == "" {
		return false
	}
	if strings.Contains(pattern, "**") {
		return matchDoubleStarGlob(pattern, path)
	}
	matched, _ := filepath.Match(pattern, path)
	if matched {
		return true
	}
	matched, _ = filepath.Match(pattern, filepath.Base(path))
	return matched
}

func matchDoubleStarGlob(pattern, path string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	prefix := strings.TrimRight(parts[0], string(filepath.Separator))
	suffix := ""
	if len(parts) > 1 {
		suffix = strings.TrimLeft(parts[1], string(filepath.Separator))
	}
	if prefix != "" {
		cleanPath := filepath.Clean(path)
		cleanPrefix := filepath.Clean(prefix)
		if !strings.HasPrefix(cleanPath, cleanPrefix+string(filepath.Separator)) && cleanPath != cleanPrefix {
			return false
		}
	}
	if suffix == "" {
		return true
	}
	matched, _ := filepath.Match(suffix, filepath.Base(path))
	return matched
}

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
		suffix := pattern[1:]
		return host == pattern[2:] || strings.HasSuffix(host, suffix)
	}
	return pattern == host
}

func matchToolName(pattern, name string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	name = strings.ToLower(strings.TrimSpace(name))
	if pattern == "" || name == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return pattern == name
}

// hostOf normalizes a URL's host as WebFetch(host) rules write it:
// lowercased, without port or trailing dot.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}
