// Package skill loads skills, prompts the model or the user can invoke, and
// renders them for an invocation.
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

// Spec is a skill.
type Spec struct {
	Name        string
	Description string
	WhenToUse   string

	// FilePath is the skill's file, read afresh at every invocation unless
	// the skill is frozen; empty for a bundled skill, whose text is built in.
	FilePath string
	// BaseDir is the directory the skill's references are relative to.
	BaseDir string
	// Source says where the skill comes from, for the user: "builtin",
	// "user" or "project".
	Source string
	// Privileged lets the skill do what it may only where trusted: run the
	// commands its text holds, allow tools and pick a model. See Privileges.
	Privileged bool

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

	text   string // the skill's file, if fixed
	frozen bool   // text is the file, which is not read afresh
}

// Forked reports whether the skill runs in a sub-agent rather than in the
// conversation.
func (s Spec) Forked() bool { return s.Context == "fork" }

// Privileges lists what the skill does that it may only where trusted: the
// commands its text runs as it is invoked, the tools it allows unasked and
// the model it picks.
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

// Freeze returns the skill fixed to its file as it is now: invoking it no
// longer reads the file afresh. A project's skills are frozen as they load,
// so that what runs is what the user trusted.
func (s Spec) Freeze() (Spec, error) {
	text, err := s.read()
	if err != nil {
		return Spec{}, err
	}
	s.text, s.frozen = text, true
	return s, nil
}

// read returns the skill's file: its text, if frozen, else read afresh.
func (s Spec) read() (string, error) {
	if s.frozen {
		return s.text, nil
	}
	data, err := regular.ReadFile(s.FilePath)
	return string(data), err
}

// prompt renders the skill for an invocation with args.
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

func stripFrontmatter(content string) string {
	_, body, _ := frontmatter.Split(content)
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

// variables are what a skill's text names as ${NAME}, and its commands as
// environment variables.
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

// expandShell replaces each !`command` in body with the command's output.
// It runs the commands as the text holds them, which Privileges lists;
// vars reach them in their environment, as values put in their text would
// run as part of them.
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
