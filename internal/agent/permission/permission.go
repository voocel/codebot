// Package permission decides whether the agent's tool calls and hook
// commands may run. It classifies each call — its capability, the paths it
// touches, whether it must be confirmed each time — and weighs it against
// the rules, the mode, the filesystem roots and the approvals the user gave,
// asking the user through interact.UI when these leave it open.
package permission

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/interact"
)

type Capability string

const (
	CapabilityRead     Capability = "read"
	CapabilityWrite    Capability = "write"
	CapabilityExec     Capability = "exec"
	CapabilityHook     Capability = "hook"
	CapabilityNetwork  Capability = "network"
	CapabilityInternal Capability = "internal"
	CapabilityUnknown  Capability = "unknown"
)

type DecisionKind string

const (
	DecisionAllow        DecisionKind = "allow"
	DecisionAllowOnce    DecisionKind = "allow_once"
	DecisionAllowSession DecisionKind = "allow_session"
	DecisionAllowAlways  DecisionKind = "allow_always"
	DecisionDeny         DecisionKind = "deny"
)

type DecisionSource string

const (
	DecisionSourceRule     DecisionSource = "rule"
	DecisionSourceGrant    DecisionSource = "grant"
	DecisionSourceMode     DecisionSource = "mode"
	DecisionSourcePrompt   DecisionSource = "prompt"
	DecisionSourceStore    DecisionSource = "store"
	DecisionSourceRoots    DecisionSource = "roots"
	DecisionSourceInternal DecisionSource = "internal"
)

// FilesystemRoots scope filesystem access for tool requests. The two pairs
// serve different audiences:
//
//   - ReadRoots / WriteRoots: user-configured. Subject to deny rules and
//     mode-based prompts (e.g. balanced mode asks for any write). Out-of-roots
//     access triggers an OutsideRoots prompt; AllowAlways for that prompt is
//     downgraded to AllowOnce so a one-shot consent does not silently grant
//     persistent access.
//
//   - InternalReadable / InternalWritable: harness-declared. Reserved for
//     paths the harness itself manages (auto-memory dir, scratch space).
//     Matches bypass the OutsideRoots prompt and the mode-based ask so the
//     agent can read/write these locations silently. Deny rules still apply.
//
// A path matched by InternalWritable is also treated as readable, so callers
// that want bidirectional access only need to populate the writable list.
type FilesystemRoots struct {
	ReadRoots        []string
	WriteRoots       []string
	InternalReadable []string
	InternalWritable []string
}

type Metadata struct {
	Capability  Capability `json:"capability,omitempty"`
	SummaryHint string     `json:"summary_hint,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	Key         string     `json:"key,omitempty"`
	KeyPrefix   string     `json:"key_prefix,omitempty"`
}

type Request struct {
	ToolID    string          `json:"tool_id,omitempty"`
	ToolName  string          `json:"tool_name"`
	ToolLabel string          `json:"tool_label,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Metadata  Metadata        `json:"metadata,omitempty"`
	// Workspace overrides the base for resolving relative operand paths on THIS
	// request; empty falls back to Config.Cwd. Set it when the cwd
	// changes per call (e.g. a worktree) so checks/audit match where tools run.
	Workspace string `json:"workspace,omitempty"`
	// Grants allow this request on top of the stored approvals, e.g. for the
	// tools an active skill allows. Deny rules, the roots and the paths
	// confirmed each time still come first.
	Grants []Rule `json:"-"`
}

type Decision struct {
	Kind         DecisionKind   `json:"kind"`
	Source       DecisionSource `json:"source,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Capability   Capability     `json:"capability,omitempty"`
	Summary      string         `json:"summary,omitempty"`
	Key          string         `json:"key,omitempty"`
	OutsideRoots bool           `json:"outside_roots,omitempty"`
	Prompted     bool           `json:"prompted,omitempty"`
}

func (d Decision) Allowed() bool {
	switch d.Kind {
	case DecisionAllow, DecisionAllowOnce, DecisionAllowSession, DecisionAllowAlways:
		return true
	default:
		return false
	}
}

type AuditEntry struct {
	Time       time.Time     `json:"time"`
	Mode       interact.Mode `json:"mode"`
	Tool       string        `json:"tool"`
	Capability Capability    `json:"capability"`
	Summary    string        `json:"summary"`
	Decision   string        `json:"decision"`
	Reason     string        `json:"reason,omitempty"`
	Allow      bool          `json:"allow"`
}

// ParseGrants parses the tools a skill allows. Entries that do not parse
// grant nothing.
func ParseGrants(raw []string) []Rule {
	var rules []Rule
	for _, r := range raw {
		if rule, err := ParseRule(r); err == nil {
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
func (h HookRequest) request() Request {
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
	return Request{
		ToolName: "hook/" + event,
		Summary:  summary,
		Reason:   reason,
		Metadata: Metadata{
			Capability: CapabilityHook,
			Key:        "hook:" + event + ":" + command,
		},
	}
}
