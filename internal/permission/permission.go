// Package permission is the policy engine that decides tool calls: rules,
// modes, filesystem roots and stored approvals, asking the user when they
// leave a call open. Package approval adapts it to the tools.
package permission

import (
	"context"
	"encoding/json"
	"time"
)

type Mode string

const (
	ModeStrict      Mode = "strict"
	ModeBalanced    Mode = "balanced"
	ModeAcceptEdits Mode = "accept-edits"
	ModeTrust       Mode = "trust"
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

type Choice string

const (
	ChoiceAllowOnce    Choice = "allow_once"
	ChoiceAllowSession Choice = "allow_session"
	ChoiceAllowAlways  Choice = "allow_always"
	ChoiceDeny         Choice = "deny"
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
	// request; empty falls back to EngineConfig.Workspace. Set it when the cwd
	// changes per call (e.g. a worktree) so checks/audit match where tools run.
	Workspace string `json:"workspace,omitempty"`
	// Grants allow this request on top of the stored approvals, e.g. for the
	// tools an active skill allows. Deny rules, the roots and the classifier's
	// Confirm still come first.
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

type Prompt struct {
	// ToolID is the ID of the tool call awaiting approval, when the request
	// came from one.
	ToolID     string
	Tool       string
	Summary    string
	Reason     string
	Capability Capability
	// OutsideRoots means the call reaches outside the filesystem roots.
	OutsideRoots bool
	// OnceOnly means only ChoiceAllowOnce or ChoiceDeny apply: the call is
	// outside the roots, or its classification asks to confirm it each time.
	// Any other allow counts as once.
	OnceOnly bool
}

type Approver func(ctx context.Context, prompt Prompt) (Choice, error)

type AuditEntry struct {
	Time       time.Time  `json:"time"`
	Mode       Mode       `json:"mode"`
	Tool       string     `json:"tool"`
	Capability Capability `json:"capability"`
	Summary    string     `json:"summary"`
	Decision   string     `json:"decision"`
	Reason     string     `json:"reason,omitempty"`
	Allow      bool       `json:"allow"`
}

// Classification describes how the permission engine should treat a tool
// call. The harness owns the mapping from tool names to capabilities and
// operand fields, and provides it via EngineConfig.Classifier.
//
// Field semantics by Capability:
//   - Read    : Path is checked against ReadRoots; populates summary.
//   - Write   : Path is checked against WriteRoots; populates summary
//     and contributes to the audit key (write:<path>).
//   - Exec    : Command populates summary; hashed into the audit key
//     (exec:<hash>). Workdir, if set, is checked against WriteRoots.
//   - Network : URL populates summary; host extracted into audit key
//     (network:<host>). Empty URL falls back to network:<tool>.
//   - Internal: no operand fields used; key is internal:<tool>.
//   - Unknown : key is tool:<tool>.
//
// Summary, Reason, and Key are optional overrides — empty fields fall
// back to engine-derived defaults.
type Classification struct {
	Capability Capability
	Path       string
	Command    string
	Workdir    string
	URL        string
	Summary    string
	Reason     string
	Key        string
	// Confirm, when set, is why the user must confirm this call each time:
	// the mode and stored approvals do not apply, and an allow covers this
	// call only. Deny rules still apply first.
	Confirm string
}

// Classifier maps a Request to a Classification. The harness owns the
// mapping from tool names to capabilities and operand fields. If nil,
// every request defaults to CapabilityUnknown unless Request.Metadata
// supplies a Capability override.
type Classifier func(req Request) Classification

type EngineConfig struct {
	Workspace string
	Mode      Mode
	Rules     *RuleSet
	Roots     FilesystemRoots
	Store     *Store
	// Approver asks the user. Without one, whatever needs asking is denied.
	Approver Approver
	OnAudit  func(AuditEntry)
	// Classifier maps tool requests to capabilities + operand fields.
	// See Classification for field semantics.
	Classifier Classifier
}
