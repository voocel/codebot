package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/config"
)

// ForkExecutor runs a task in a forked subagent context.
// It matches the SubAgentTool.Execute signature so it can be wired directly.
type ForkExecutor func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)

// AllowToolsSetter grants temporary tool permissions for the active skill.
// Called with nil/empty to clear grants from the previous skill.
type AllowToolsSetter func(tools []string)

// SkillTool lets the LLM invoke skills by name.
// It loads the skill file, strips frontmatter, expands $ARGUMENTS placeholders,
// and returns the formatted content as a tool result.
// Skills with context: fork are delegated to a subagent via the ForkExecutor.
type SkillTool struct {
	mu               sync.RWMutex
	skills           []config.Skill
	sessionID        string
	forkExecutor     ForkExecutor
	allowToolsSetter AllowToolsSetter
}

// NewSkillTool creates a SkillTool with the given initial skill list.
func NewSkillTool(skills []config.Skill, sessionID string) *SkillTool {
	return &SkillTool{skills: skills, sessionID: sessionID}
}

// SetSkills replaces the skill list (called on /reload).
func (t *SkillTool) SetSkills(skills []config.Skill) {
	t.mu.Lock()
	t.skills = skills
	t.mu.Unlock()
}

// SetForkExecutor sets the function used to run context: fork skills in a subagent.
func (t *SkillTool) SetForkExecutor(fn ForkExecutor) {
	t.forkExecutor = fn
}

// SetAllowToolsSetter sets the function used to grant temporary tool permissions.
func (t *SkillTool) SetAllowToolsSetter(fn AllowToolsSetter) {
	t.allowToolsSetter = fn
}

func (t *SkillTool) Name() string  { return "Skill" }
func (t *SkillTool) Label() string { return "Skill" }

func (t *SkillTool) Description() string {
	return `Execute a skill within the conversation.

Available skills are listed in system-reminder messages. When a user's task matches a skill description, or when they reference a skill by name (e.g. "/commit"), invoke this tool with the skill name and optional arguments.

How to invoke:
- skill: "commit" — invoke the commit skill
- skill: "commit", args: "-m 'Fix bug'" — invoke with arguments
- skill: "review-pr", args: "123" — invoke with arguments

Important:
- When a matching skill exists, this is a BLOCKING REQUIREMENT: invoke this tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Only invoke skills listed in system reminders; do NOT guess skill names
- Do not invoke a skill that is already running
- Do not use this tool for built-in commands (/help, /clear, /model, etc.)
- If you see a <skill> tag in the current conversation turn, the skill has ALREADY been loaded — follow the instructions directly instead of calling this tool again`
}

func (t *SkillTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("skill", schema.String("The skill name, e.g. \"commit\", \"review-pr\"")).Required(),
		schema.Property("args", schema.String("Optional arguments for the skill")),
	)
}

type skillArgs struct {
	Skill string `json:"skill"`
	Args  string `json:"args"`
}

func (t *SkillTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a skillArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	name := strings.ToLower(strings.TrimSpace(a.Skill))
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}

	t.mu.RLock()
	var found config.Skill
	var ok bool
	for _, s := range t.skills {
		if s.Name == name {
			found = s
			ok = true
			break
		}
	}
	t.mu.RUnlock()

	if !ok {
		return json.Marshal(fmt.Sprintf(
			"Skill %q not found. Check available skills in system-reminder messages.", name))
	}

	if found.DisableModelInvocation {
		return json.Marshal(fmt.Sprintf(
			"Skill %q is configured for manual invocation only. The user can invoke it with /%s.", name, name))
	}

	data, err := os.ReadFile(found.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read skill %q: %w", name, err)
	}

	body := strings.TrimSpace(config.StripFrontmatter(string(data)))
	body = ExpandSkillVars(body, found.BaseDir, t.sessionID)
	body = ExpandShellInjections(body)
	body = ExpandSkillArgs(body, a.Args)

	// context: fork — delegate to a subagent.
	if found.Context == "fork" && t.forkExecutor != nil {
		agentType := normalizeAgentType(found.Agent)
		forkParams := map[string]string{
			"agent": agentType,
			"task":  body,
		}
		if found.Model != "" {
			forkParams["model"] = found.Model
		}
		forkArgs, _ := json.Marshal(forkParams)
		return t.forkExecutor(ctx, forkArgs)
	}

	// Grant temporary tool permissions from allowed-tools.
	if t.allowToolsSetter != nil {
		t.allowToolsSetter(found.AllowedTools) // nil/empty clears previous grants
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<skill name=%q>\n", found.Name)
	fmt.Fprintf(&sb, "References are relative to %s.\n\n", found.BaseDir)
	sb.WriteString(body)
	sb.WriteString("\n</skill>")

	return json.Marshal(sb.String())
}

// reSkillIndexed matches $ARGUMENTS[0], $ARGUMENTS[1], etc.
var reSkillIndexed = regexp.MustCompile(`\$ARGUMENTS\[(\d{1,2})\]`)

// reSkillPositional matches $0, $1, ..., $99.
var reSkillPositional = regexp.MustCompile(`\$(\d{1,2})(?:\b|$)`)

// ExpandSkillArgs substitutes $ARGUMENTS, $@, $N, and $ARGUMENTS[N] in body.
// If none of these placeholders exist and args is non-empty, appends "ARGUMENTS: <args>".
func ExpandSkillArgs(body, rawArgs string) string {
	if rawArgs == "" {
		return body
	}

	hasPlaceholder := strings.Contains(body, "$ARGUMENTS") ||
		strings.Contains(body, "$@") ||
		reSkillPositional.MatchString(body)

	if !hasPlaceholder {
		return body + "\n\nARGUMENTS: " + rawArgs
	}

	parts := splitSkillArgs(rawArgs)

	// 1. $ARGUMENTS[N] — indexed access (must run before $ARGUMENTS replacement)
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

	// 2. $N — positional shorthand (0-based)
	result = reSkillPositional.ReplaceAllStringFunc(result, func(m string) string {
		idx, _ := strconv.Atoi(strings.TrimPrefix(m, "$"))
		if idx < 0 || idx >= len(parts) {
			return ""
		}
		return parts[idx]
	})

	// 3. $ARGUMENTS and $@ — all args joined
	result = strings.ReplaceAll(result, "$ARGUMENTS", rawArgs)
	result = strings.ReplaceAll(result, "$@", rawArgs)

	return result
}

// ExpandSkillVars substitutes ${CODEBOT_SKILL_DIR} and ${CODEBOT_SESSION_ID} in body.
// Also supports the official ${CLAUDE_SKILL_DIR} / ${CLAUDE_SESSION_ID} aliases for compatibility.
func ExpandSkillVars(body, skillDir, sessionID string) string {
	r := strings.NewReplacer(
		"${CODEBOT_SKILL_DIR}", skillDir,
		"${CODEBOT_SESSION_ID}", sessionID,
		"${CLAUDE_SKILL_DIR}", skillDir,
		"${CLAUDE_SESSION_ID}", sessionID,
	)
	return r.Replace(body)
}

// reShellInjection matches !`command` syntax for shell preprocessing.
var reShellInjection = regexp.MustCompile("!`([^`]+)`")

// ExpandShellInjections executes !`command` placeholders and replaces them with output.
// Commands run in the current working directory with a short timeout.
// On error, the placeholder is replaced with an error message.
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

// normalizeAgentType maps official agent type names to codebot's registered types.
func normalizeAgentType(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "explore":
		return "explore"
	case "plan":
		return "plan"
	case "coder", "general-purpose":
		return "coder"
	case "":
		return "coder"
	default:
		return strings.ToLower(agent)
	}
}

// splitSkillArgs splits an argument string respecting double and single quotes.
func splitSkillArgs(s string) []string {
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
