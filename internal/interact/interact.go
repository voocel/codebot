// Package interact is the contract between the core and its frontends. A new
// frontend implements UI and needs no change in the core.
package interact

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrUnsupported is returned by a headless UI that cannot ask questions.
var ErrUnsupported = errors.New("interaction not supported by this frontend")

type UI interface {
	// Approve is called for tool calls and hook commands the permission mode
	// does not allow on its own.
	Approve(ctx context.Context, req Approval) (Verdict, error)
	// Ask serves ask_user.
	Ask(ctx context.Context, qs []Question) (Answers, error)
}

type Approval struct {
	// ToolID is empty when the request is not for a tool call, e.g. a hook
	// command.
	ToolID  string
	Tool    string
	Summary string
	// Intent is the call's own account of what it does, such as a command's
	// description. It is the model's word, so frontends show it beside what
	// the call does, never instead of it.
	Intent string
	// Dir is the absolute directory a command runs in when the call names
	// one other than the workspace.
	Dir    string
	Reason string
	// Warning describes what a destructive command would do; frontends show
	// it prominently.
	Warning      string
	OutsideRoots bool
	// Confirm means no mode or stored approval can ever allow this call, so
	// it is asked every time.
	Confirm bool
	// Remember describes what AllowAlways allows from now on, e.g. "`go
	// test` commands in this project". Empty means only AllowOnce applies.
	Remember string
	// Edit means the call edits files, which accept-edits mode allows
	// without asking.
	Edit bool
}

type Verdict struct {
	Choice Choice
	// Feedback accompanies a denial: what the user wants done instead.
	Feedback string
}

type Choice string

const (
	AllowOnce Choice = "allow_once"
	// AllowAlways also allows what Approval.Remember describes.
	AllowAlways Choice = "allow_always"
	Deny        Choice = "deny"
)

type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	// Custom offers a free-text answer below the options; nil means true.
	Custom *bool `json:"custom,omitempty"`
}

func (q Question) AllowsCustom() bool {
	return q.Custom == nil || *q.Custom
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"`
}

type Answers struct {
	// Selected maps each answered question to the chosen labels, or to the
	// typed answer.
	Selected map[string][]string
	// Notes maps a question to text typed beside a choice.
	Notes     map[string]string
	Cancelled bool
}

// Mode is how much the agent may do without asking.
type Mode string

const (
	ModeStrict      Mode = "strict"
	ModeBalanced    Mode = "balanced"
	ModeAcceptEdits Mode = "accept-edits"
	ModeTrust       Mode = "trust"
)

// Modes is in Shift+Tab and ACP order.
var Modes = []Mode{ModeStrict, ModeBalanced, ModeAcceptEdits, ModeTrust}

func ParseMode(raw string) (Mode, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(raw)))
	if !slices.Contains(Modes, m) {
		return "", fmt.Errorf("invalid mode %q (allowed: strict, balanced, accept-edits, trust)", raw)
	}
	return m, nil
}
