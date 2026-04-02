package skill

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type GetPromptFn func(ctx context.Context, args string, sessionID string) (string, error)

type HookEntry struct {
	Type     string `yaml:"type" json:"type"`
	Command  string `yaml:"command" json:"command"`
	Matcher  string `yaml:"matcher,omitempty" json:"matcher,omitempty"`
	Blocking *bool  `yaml:"blocking,omitempty" json:"blocking,omitempty"`
	Timeout  *int   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type HooksConfig map[string][]HookEntry

type Spec struct {
	Name        string
	Description string
	WhenToUse   string
	Version     string

	FilePath string
	BaseDir  string
	Source   string

	DisableModelInvocation bool
	DisableUserInvocation  bool

	ArgumentHint  string
	ArgumentNames []string

	Context string
	Agent   string
	Model   string
	Effort  string

	AllowedTools []string
	Paths        []string
	Hooks        HooksConfig

	HasExplicitDescription bool
	FrontmatterKeys        []string

	GetPrompt GetPromptFn
}

type Delta struct {
	AllowedTools  []string
	ModelOverride string
	Effort        string
	Paths         []string
	Hooks         HooksConfig
}

type InvocationMode string

const (
	ModeInline InvocationMode = "inline"
	ModeFork   InvocationMode = "fork"
)

type InvocationSource string

const (
	SourceModel InvocationSource = "model"
	SourceUser  InvocationSource = "user"
)

type InvocationResult struct {
	Spec       Spec
	Mode       InvocationMode
	PromptText string
	Agent      string
	Delta      Delta
}

var reSkillName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	return reSkillName.MatchString(strings.ToLower(strings.TrimSpace(name)))
}

func NormalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func StripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return content
	}
	rest := content[4:]
	_, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return content
	}
	return strings.TrimLeft(after, "\r\n")
}

func FirstLine(s string, maxLen int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > maxLen {
			return string(runes[:maxLen])
		}
		return line
	}
	return ""
}

func WrapPrompt(spec Spec, body string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<skill name=%q>\n", spec.Name)
	fmt.Fprintf(&sb, "References are relative to %s.\n\n", spec.BaseDir)
	sb.WriteString(strings.TrimSpace(body))
	sb.WriteString("\n</skill>")
	return sb.String()
}

var reSkillIndexed = regexp.MustCompile(`\$ARGUMENTS\[(\d{1,2})\]`)
var reSkillPositional = regexp.MustCompile(`\$(\d{1,2})(?:\b|$)`)

func ExpandArgs(body, rawArgs string) string {
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
	result := reSkillIndexed.ReplaceAllStringFunc(body, func(m string) string {
		sub := reSkillIndexed.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		idx, _ := strconv.Atoi(sub[1])
		if idx < 0 || idx >= len(parts) {
			return ""
		}
		return parts[idx]
	})
	result = reSkillPositional.ReplaceAllStringFunc(result, func(m string) string {
		idx, _ := strconv.Atoi(strings.TrimPrefix(m, "$"))
		if idx < 0 || idx >= len(parts) {
			return ""
		}
		return parts[idx]
	})
	result = strings.ReplaceAll(result, "$ARGUMENTS", rawArgs)
	result = strings.ReplaceAll(result, "$@", rawArgs)
	return result
}

func ExpandVars(body, skillDir, sessionID string) string {
	r := strings.NewReplacer(
		"${CODEBOT_SKILL_DIR}", skillDir,
		"${CODEBOT_SESSION_ID}", sessionID,
		"${CLAUDE_SKILL_DIR}", skillDir,
		"${CLAUDE_SESSION_ID}", sessionID,
	)
	return r.Replace(body)
}

var reShellInjection = regexp.MustCompile("!`([^`]+)`")

func ExpandShellInjections(body string) string {
	return reShellInjection.ReplaceAllStringFunc(body, func(m string) string {
		sub := reShellInjection.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", sub[1])
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("[error: %s]", err)
		}
		return strings.TrimRight(string(out), "\n")
	})
}

func SourceAllowsShellExecution(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "bundled", "project", "user":
		return true
	default:
		return false
	}
}

func SourceAllowsPrivilegedFields(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "bundled", "project", "user":
		return true
	default:
		return false
	}
}

func NormalizeAgentType(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "explore":
		return "explore"
	case "plan":
		return "plan"
	case "coder", "general-purpose", "":
		return "coder"
	default:
		return strings.TrimSpace(agent)
	}
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
