// Package skill loads skills, prompts that the model or the user can invoke,
// and renders them for an invocation.
package skill

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/lib/detached"
	"github.com/voocel/codebot/internal/lib/frontmatter"
	"github.com/voocel/codebot/internal/lib/regular"
)

type Spec struct {
	Name        string
	Description string
	WhenToUse   string

	// FilePath is reread on every invocation unless the skill is frozen.
	// Bundled skills have none.
	FilePath string
	// BaseDir is where the skill's relative references resolve.
	BaseDir string
	// Source is "builtin", "user" or "project", shown to the user.
	Source string
	// Privileged lets the skill run its commands, allow tools and pick a
	// model. Only trusted skills get it.
	Privileged bool

	DisableModelInvocation bool
	DisableUserInvocation  bool
	ArgumentHint           string

	// Context is "fork" for a skill that runs in a sub-agent, else "inline".
	Context string
	Agent   string
	// Model overrides the model of a forked skill's sub-agent.
	Model        string
	AllowedTools []string
	// Paths, if set, activate the skill only in a workspace containing a
	// match.
	Paths []string

	text   string // the file's content when frozen
	frozen bool
}

func (s Spec) Forked() bool { return s.Context == "fork" }

// Privileges lists, for the user to review, what the skill does only when
// privileged: the commands it runs, the tools it allows and the model it
// picks.
func (s Spec) Privileges() []string {
	var out []string
	if text, err := s.read(); err == nil {
		for _, m := range reShellInjection.FindAllStringSubmatch(stripFrontmatter(text), -1) {
			out = append(out, "runs `"+m[1]+"`")
		}
	}
	for _, tool := range s.AllowedTools {
		out = append(out, "allows `"+tool+"`")
	}
	if s.Model != "" {
		out = append(out, "picks "+s.Model)
	}
	return out
}

// Freeze snapshots the file so later edits don't change the skill. Project
// skills are frozen on load, so what runs is what the user trusted.
func (s Spec) Freeze() (Spec, error) {
	text, err := s.read()
	if err != nil {
		return Spec{}, err
	}
	s.text, s.frozen = text, true
	return s, nil
}

func (s Spec) read() (string, error) {
	if s.frozen {
		return s.text, nil
	}
	data, err := regular.ReadFile(s.FilePath)
	return string(data), err
}

func (s Spec) prompt(ctx context.Context, args, sessionID string) (string, error) {
	text, err := s.read()
	if err != nil {
		return "", err
	}
	vars := variables(s.BaseDir, sessionID)
	body := stripFrontmatter(text)
	if s.Privileged {
		body = expandShell(ctx, body, vars)
	}
	body = expandArgs(expandVars(body, vars), args)
	return fmt.Sprintf("<skill name=%q>\nReferences are relative to %s.\n\n%s\n</skill>", s.Name, s.BaseDir, strings.TrimSpace(body)), nil
}

var reSkillName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	return reSkillName.MatchString(normalizeName(name))
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func stripFrontmatter(content string) string {
	_, body, _ := frontmatter.Split(content)
	return strings.TrimSpace(body)
}

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

// expandArgs replaces $ARGUMENTS and $@ with all arguments, and $ARGUMENTS[n]
// and $n with the nth. Without placeholders the arguments are appended.
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

// variables are available as ${NAME} in the skill's text and as environment
// variables to its commands.
func variables(skillDir, sessionID string) map[string]string {
	return map[string]string{
		"CODEBOT_SKILL_DIR":  skillDir,
		"CODEBOT_SESSION_ID": sessionID,
		"CLAUDE_SKILL_DIR":   skillDir,
		"CLAUDE_SESSION_ID":  sessionID,
	}
}

func expandVars(body string, vars map[string]string) string {
	var pairs []string
	for k, v := range vars {
		pairs = append(pairs, "${"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(body)
}

var reShellInjection = regexp.MustCompile("!`([^`]+)`")

// expandShell replaces each !`command` with its output. Variables go in the
// environment rather than the command text, so their values can't inject
// shell code.
func expandShell(ctx context.Context, body string, vars map[string]string) string {
	env := os.Environ()
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	return reShellInjection.ReplaceAllStringFunc(body, func(m string) string {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := detached.Command(ctx, "sh", "-c", reShellInjection.FindStringSubmatch(m)[1])
		cmd.Env = env
		out, err := cmd.CombinedOutput()
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
