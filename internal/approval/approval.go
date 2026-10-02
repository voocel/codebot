// Package approval is codebot's permission policy over the permission
// engine: which capability each tool has, the paths that always need
// confirming, and how the user is asked.
package approval

import (
	"strings"

	"github.com/voocel/codebot/internal/permission"
)

type (
	AuditEntry      = permission.AuditEntry
	FilesystemRoots = permission.FilesystemRoots
	Rule            = permission.Rule
	RuleSet         = permission.RuleSet
)

func ParseRuleSet(allow, deny []string) (*RuleSet, error) {
	return permission.ParseRuleSet(allow, deny)
}

// ParseGrants parses the tools a skill allows. Entries that do not parse
// grant nothing.
func ParseGrants(raw []string) []Rule {
	var rules []Rule
	for _, r := range raw {
		if rule, err := permission.ParseRule(r); err == nil {
			rules = append(rules, rule)
		}
	}
	return rules
}

// HookRequest is a hook command about to run.
type HookRequest struct {
	Event    string
	Tool     string
	Command  string
	Blocking bool
}

// request is how the engine sees a hook command: a tool of its own, with the
// hook capability, remembered per event and command.
func (h HookRequest) request() permission.Request {
	event := strings.ToLower(strings.TrimSpace(firstNonEmpty(h.Event, "unknown")))
	command := strings.TrimSpace(h.Command)
	summary := command
	switch {
	case h.Tool != "":
		summary = h.Event + " (" + h.Tool + ") -> " + command
	case h.Event != "":
		summary = h.Event + " -> " + command
	}
	reason := "hook command requires approval"
	if h.Blocking {
		reason = "blocking hook command requires approval"
	}
	return permission.Request{
		ToolName: "hook/" + event,
		Summary:  summary,
		Reason:   reason,
		Metadata: permission.Metadata{
			Capability: permission.CapabilityHook,
			Key:        "hook:" + event + ":" + command,
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
