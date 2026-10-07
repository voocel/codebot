// Package permission decides whether tool calls and hook commands may run.
// It classifies each call and checks it against the rules, the mode, the
// filesystem roots and stored approvals, asking the user through
// interact.UI when none of them decides.
package permission

import (
	"encoding/json"
	"time"

	"github.com/voocel/codebot/internal/interact"
)

type Capability string

const (
	CapabilityRead     Capability = "read"
	CapabilityWrite    Capability = "write"
	CapabilityExec     Capability = "exec"
	CapabilityNetwork  Capability = "network"
	CapabilityInternal Capability = "internal"
	CapabilityUnknown  Capability = "unknown"
)

type DecisionKind string

const (
	DecisionAllow       DecisionKind = "allow"
	DecisionAllowOnce   DecisionKind = "allow_once"
	DecisionAllowAlways DecisionKind = "allow_always"
	DecisionDeny        DecisionKind = "deny"
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

// FilesystemRoots scope filesystem access.
//
// ReadRoots and WriteRoots come from the user. Access outside them is
// confirmed each time, so one consent never grants lasting access.
//
// InternalReadable and InternalWritable are paths the harness manages, such
// as the memory dir. Access there skips the outside-roots and mode prompts;
// deny rules still apply. An InternalWritable path is also readable.
type FilesystemRoots struct {
	ReadRoots        []string
	WriteRoots       []string
	InternalReadable []string
	InternalWritable []string
	// Protected directories hold files that decide what codebot runs, such
	// as local plugins. Every write there is confirmed, in any mode.
	Protected []string
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
	// Workspace resolves relative paths for this request; empty means
	// Config.Cwd. Set it when the tool runs elsewhere, e.g. in a worktree.
	Workspace string `json:"workspace,omitempty"`
	// Grants allow this request on top of stored approvals, e.g. the tools an
	// active skill allows. Deny rules, roots and confirm-each-time paths
	// still come first.
	Grants []Rule `json:"-"`
}

type Decision struct {
	Kind         DecisionKind   `json:"kind"`
	Source       DecisionSource `json:"source,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Capability   Capability     `json:"capability,omitempty"`
	Summary      string         `json:"summary,omitempty"`
	OutsideRoots bool           `json:"outside_roots,omitempty"`
	Prompted     bool           `json:"prompted,omitempty"`
}

func (d Decision) Allowed() bool {
	switch d.Kind {
	case DecisionAllow, DecisionAllowOnce, DecisionAllowAlways:
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

// ParseGrants skips entries that do not parse.
func ParseGrants(raw []string) []Rule {
	var rules []Rule
	for _, r := range raw {
		if rule, err := ParseRule(r); err == nil {
			rules = append(rules, rule)
		}
	}
	return rules
}
