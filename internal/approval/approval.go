package approval

import (
	"context"
	"strings"

	"github.com/voocel/agentcore/permission"
)

type Mode = permission.Mode

const (
	ModeStrict      = permission.ModeStrict
	ModeBalanced    = permission.ModeBalanced
	ModeAcceptEdits = permission.ModeAcceptEdits
	ModeTrust       = permission.ModeTrust
)

type Capability = permission.Capability

const (
	CapHook     = permission.CapabilityHook
	CapInternal = permission.CapabilityInternal
)

type Choice = permission.Choice

const (
	ChoiceAllowOnce    = permission.ChoiceAllowOnce
	ChoiceAllowSession = permission.ChoiceAllowSession
	ChoiceAllowAlways  = permission.ChoiceAllowAlways
	ChoiceDeny         = permission.ChoiceDeny
)

type Prompt = permission.Prompt
type ApproverFunc = permission.Approver
type FilesystemRoots = permission.FilesystemRoots
type AuditEntry = permission.AuditEntry
type Rule = permission.Rule
type RuleSet = permission.RuleSet
type storedEntry = permission.StoreEntry

func ParseMode(raw string) (Mode, error) {
	return permission.ParseMode(raw)
}

func ParseRuleSet(allow, deny []string) (*RuleSet, error) {
	return permission.ParseRuleSet(allow, deny)
}

func ParseRule(raw string) (Rule, error) {
	return permission.ParseRule(raw)
}

type CommandCategory string

const (
	CommandCategoryInfo    CommandCategory = "info"
	CommandCategoryPrompt  CommandCategory = "prompt"
	CommandCategorySession CommandCategory = "session"
	CommandCategoryConfig  CommandCategory = "config"
	CommandCategoryPlan    CommandCategory = "plan"
	CommandCategoryExit    CommandCategory = "exit"
)

type CommandRequest struct {
	Name      string
	Category  CommandCategory
	NeedsIdle bool
	IsRunning bool
	Summary   string
	Preview   string
}

type HookRequest struct {
	Event    string
	Tool     string
	Command  string
	Blocking bool
}

type toolInfo struct {
	tool       string
	capability Capability
	summary    string
	preview    string
	reason     string
	key        string
}

func NormalizeCommandCategory(raw string) CommandCategory {
	return normalizeCommandCategory(CommandCategory(strings.ToLower(strings.TrimSpace(raw))))
}

func normalizeCommandCategory(category CommandCategory) CommandCategory {
	switch category {
	case CommandCategoryInfo, CommandCategorySession, CommandCategoryConfig, CommandCategoryPlan, CommandCategoryExit:
		return category
	default:
		return CommandCategoryPrompt
	}
}

func inspectHook(req HookRequest) toolInfo {
	event := firstNonEmpty(req.Event, "unknown")
	summary := strings.TrimSpace(req.Command)
	if req.Event != "" {
		summary = strings.TrimSpace(req.Event + " -> " + req.Command)
	}
	if req.Tool != "" {
		summary = strings.TrimSpace(req.Event + " (" + req.Tool + ") -> " + req.Command)
	}
	reason := "hook command requires approval"
	if req.Blocking {
		reason = "blocking hook command requires approval"
	}
	return toolInfo{
		tool:       "hook/" + strings.ToLower(strings.TrimSpace(event)),
		capability: CapHook,
		summary:    summary,
		preview:    strings.TrimSpace(req.Command),
		reason:     reason,
		key:        "hook:" + strings.ToLower(strings.TrimSpace(event)) + ":" + permissionKey(req.Command),
	}
}

func inspectCommand(req CommandRequest) toolInfo {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = "/" + name
	}
	return toolInfo{
		tool:       "command/" + name,
		capability: CapInternal,
		summary:    summary,
		preview:    truncate(strings.TrimSpace(req.Preview), 400),
		key:        "command:" + string(normalizeCommandCategory(req.Category)) + ":" + name,
	}
}

type decisionEngine interface {
	Decide(ctx context.Context, req permission.Request) (*permission.Decision, error)
	SetFilesystemRoots(roots permission.FilesystemRoots)
	FilesystemRoots() permission.FilesystemRoots
	SetMode(mode permission.Mode)
	Mode() permission.Mode
	SetPlanMode(active bool)
	PlanMode() bool
	SetApprover(fn permission.Approver)
	SetSkillAllows(rawTools []string)
}

func truncate(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func permissionKey(input string) string {
	return strings.TrimSpace(input)
}
