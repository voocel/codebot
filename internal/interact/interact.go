// Package interact is the contract between the core and its frontends. Each
// frontend implements UI; the permission engine and the tools that need an
// answer call it, so adding a frontend means implementing this interface and
// nothing in the core. Mode is the permission mode a frontend switches.
package interact

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrUnsupported is returned by a UI that cannot ask questions, such as a
// headless run.
var ErrUnsupported = errors.New("interaction not supported by this frontend")

// UI is implemented by each frontend.
type UI interface {
	// Approve asks for consent to a tool call or hook command the
	// permission mode does not allow on its own.
	Approve(ctx context.Context, req Approval) (Verdict, error)
	// Ask poses multi-choice questions, for ask_user.
	Ask(ctx context.Context, qs []Question) (Answers, error)
}

// Approval is a request for consent.
type Approval struct {
	// ToolID is the ID of the tool call awaiting approval; empty when the
	// request does not come from one, such as a hook command.
	ToolID  string
	Tool    string
	Summary string
	Reason  string
	// Warning names what a destructive command would do, for the frontend
	// to show prominently.
	Warning string
	// OutsideRoots means the call reaches outside the workspace.
	OutsideRoots bool
	// Confirm means the call is confirmed each time: no mode or stored
	// approval allows it.
	Confirm bool
	// Remember says what AllowAlways allows from now on, "`go test`
	// commands in this project"; empty when only this call may be allowed.
	Remember string
	// Edit means the call edits files, which the accept-edits mode allows
	// without asking.
	Edit bool
}

// Verdict is the user's answer to an Approval.
type Verdict struct {
	Choice Choice
	// Feedback is what the user said to do instead, with a denial.
	Feedback string
}

// Choice is what the user chose for an Approval.
type Choice string

const (
	AllowOnce Choice = "allow_once"
	// AllowAlways allows the call and what Approval.Remember says.
	AllowAlways Choice = "allow_always"
	Deny        Choice = "deny"
)

// Question is a single multi-choice question.
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	// Custom controls whether the frontend offers a "Type your own answer"
	// entry below the options. Nil means true.
	Custom *bool `json:"custom,omitempty"`
}

// AllowsCustom reports whether the frontend should offer a free-text answer.
func (q Question) AllowsCustom() bool {
	return q.Custom == nil || *q.Custom
}

// Option is a selectable choice for a question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"`
}

// Answers is the user's response to a set of questions.
type Answers struct {
	// Selected maps each answered question to the chosen labels, or to the
	// text the user typed.
	Selected map[string][]string
	// Notes maps a question to free text the user typed beside a choice.
	Notes map[string]string
	// Cancelled is set when the user dismissed the questions.
	Cancelled bool
}

// Mode is the permission mode: how much the agent may do without asking.
type Mode string

const (
	ModeStrict      Mode = "strict"
	ModeBalanced    Mode = "balanced"
	ModeAcceptEdits Mode = "accept-edits"
	ModeTrust       Mode = "trust"
)

// Modes lists every mode in Shift+Tab / ACP order.
var Modes = []Mode{ModeStrict, ModeBalanced, ModeAcceptEdits, ModeTrust}

// ParseMode parses a mode name.
func ParseMode(raw string) (Mode, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(raw)))
	if !slices.Contains(Modes, m) {
		return "", fmt.Errorf("invalid mode %q (allowed: strict, balanced, accept-edits, trust)", raw)
	}
	return m, nil
}
