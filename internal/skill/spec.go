// Package skill loads skills, prompts the model or the user can invoke, and
// renders them for an invocation.
package skill

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Spec is a skill.
type Spec struct {
	Name        string
	Description string
	WhenToUse   string

	// FilePath is the skill's file, read afresh at every invocation; empty
	// for a bundled skill, whose text is built in.
	FilePath string
	// BaseDir is the directory the skill's references are relative to.
	BaseDir string
	// Source is where the skill comes from: "bundled", "project" or "user",
	// or "remote" for an untrusted plugin, whose skills may not run shell
	// commands, grant tools or choose a model.
	Source string

	DisableModelInvocation bool
	DisableUserInvocation  bool
	ArgumentHint           string

	// Context is "fork" for a skill that runs in a sub-agent, else "inline".
	Context string
	// Agent is the sub-agent a forked skill runs in.
	Agent string
	// Model runs a forked skill's sub-agent on a different model.
	Model        string
	AllowedTools []string
	// Paths, when set, make the skill active only in a workspace holding a
	// match.
	Paths []string

	text string // a bundled skill's file
}

// trusted reports whether the skill comes from somewhere the user trusts.
func (s Spec) trusted() bool {
	switch s.Source {
	case "bundled", "project", "user":
		return true
	}
	return false
}

// prompt renders the skill for an invocation with args.
func (s Spec) prompt(ctx context.Context, args, sessionID string) (string, error) {
	text := s.text
	if s.FilePath != "" {
		data, err := os.ReadFile(s.FilePath)
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	body := expandVars(stripFrontmatter(text), s.BaseDir, sessionID)
	if s.trusted() {
		body = expandShell(ctx, body)
	}
	body = expandArgs(body, args)
	return fmt.Sprintf("<skill name=%q>\nReferences are relative to %s.\n\n%s\n</skill>", s.Name, s.BaseDir, strings.TrimSpace(body)), nil
}

var reSkillName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

// ValidName reports whether name, ignoring case, can name a skill.
func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	return reSkillName.MatchString(normalizeName(name))
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// splitFrontmatter returns the YAML between a leading pair of "---" lines,
// and what follows it.
func splitFrontmatter(content string) (frontmatter, body string) {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return "", content
	}
	frontmatter, after, ok := strings.Cut(content[4:], "\n---")
	if !ok {
		return "", content
	}
	return frontmatter, strings.TrimLeft(after, "\r\n")
}

func stripFrontmatter(content string) string {
	_, body := splitFrontmatter(content)
	return strings.TrimSpace(body)
}

// firstLine returns the first non-blank line of s, cut to maxLen runes.
func firstLine(s string, maxLen int) string {
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > maxLen {
			return string(runes[:maxLen])
		}
		return line
	}
	return ""
}

var reSkillIndexed = regexp.MustCompile(`\$ARGUMENTS\[(\d{1,2})\]`)
var reSkillPositional = regexp.MustCompile(`\$(\d{1,2})(?:\b|$)`)

// expandArgs puts the invocation's arguments into the skill's body: $ARGUMENTS
// and $@ take them whole, $ARGUMENTS[n] and $n the nth. A body with no
// placeholder gets them appended.
func expandArgs(body, rawArgs string) string {
	if rawArgs == "" {
		return body
	}

	hasPlaceholder := strings.Contains(body, "$ARGUMENTS") ||
		strings.Contains(body, "$@") ||
		reSkillPositional.MatchString(body)

	if !hasPlaceholder {
		return body + "\n\nARGUMENTS: " + rawArgs
	}

	parts := splitArgs(rawArgs)
	arg := func(index string) string {
		idx, _ := strconv.Atoi(index)
		if idx >= len(parts) {
			return ""
		}
		return parts[idx]
	}
	result := reSkillIndexed.ReplaceAllStringFunc(body, func(m string) string {
		return arg(reSkillIndexed.FindStringSubmatch(m)[1])
	})
	result = reSkillPositional.ReplaceAllStringFunc(result, func(m string) string {
		return arg(strings.TrimPrefix(m, "$"))
	})
	result = strings.ReplaceAll(result, "$ARGUMENTS", rawArgs)
	result = strings.ReplaceAll(result, "$@", rawArgs)
	return result
}

func expandVars(body, skillDir, sessionID string) string {
	r := strings.NewReplacer(
		"${CODEBOT_SKILL_DIR}", skillDir,
		"${CODEBOT_SESSION_ID}", sessionID,
		"${CLAUDE_SKILL_DIR}", skillDir,
		"${CLAUDE_SESSION_ID}", sessionID,
	)
	return r.Replace(body)
}

var reShellInjection = regexp.MustCompile("!`([^`]+)`")

// expandShell replaces each !`command` in body with the command's output.
func expandShell(ctx context.Context, body string) string {
	return reShellInjection.ReplaceAllStringFunc(body, func(m string) string {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sh", "-c", reShellInjection.FindStringSubmatch(m)[1]).CombinedOutput()
		if err != nil {
			return fmt.Sprintf("[error: %s]", err)
		}
		return strings.TrimRight(string(out), "\n")
	})
}

func splitArgs(raw string) []string {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false

	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}
