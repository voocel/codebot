// Package commands holds the slash-command implementations and the abstractions
// required to register them with the host UI.
//
// Design: each command declares its dependencies explicitly — either via a
// New* constructor's parameters, or as exported fields on a command struct
// constructed with a literal at registration. There is no shared "Deps"
// interface — the registration site doubles as the dependency graph.
package commands

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Kind classifies how a command was contributed.
type Kind string

const (
	KindBuiltin Kind = "builtin"
	KindSkill   Kind = "skill"
)

// Spec describes command metadata used by the registry, palette, and help command.
type Spec struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
	NeedsIdle   bool
	Kind        Kind
	Source      string
}

// Invocation is the parsed slash-command input passed to command handlers.
type Invocation struct {
	Input   string
	Name    string
	RawArgs string
	Args    []string
}

// Command is the unified abstraction for all slash commands.
type Command interface {
	Spec() Spec
	Run(inv Invocation) tea.Cmd
}

// NewSimple wraps a Spec and a run function as a Command. Used by commands
// whose entire behavior fits in a single function; complex commands (those
// that own modal state, interactive overlays, or multi-step dispatchers)
// implement Command/InteractiveCommand directly with their own struct.
func NewSimple(spec Spec, run func(inv Invocation) tea.Cmd) Command {
	return &simpleCmd{spec: spec, run: run}
}

type simpleCmd struct {
	spec Spec
	run  func(inv Invocation) tea.Cmd
}

func (c *simpleCmd) Spec() Spec                 { return c.spec }
func (c *simpleCmd) Run(inv Invocation) tea.Cmd { return c.run(inv) }

// InteractiveCommand extends Command with modal keyboard interception and
// custom rendering. When Active() returns true the host TUI routes all
// keyboard events through HandleKey and replaces the input area with View.
//
// View receives the available width AND height of the terminal viewport so
// implementations can clip or paginate their content. A height of 0 means
// "unconstrained" (legacy callers); implementations should treat any positive
// value as a hard upper bound to avoid having their headers scroll out of view.
type InteractiveCommand interface {
	Command
	Active() bool
	HandleKey(msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	View(width, height int) string
	Dismiss()
}

// OverlayController is the surface needed to install or dismiss the active
// modal overlay. Most interactive commands (/btw, /context, /tasks, ...)
// take this directly because they never enumerate peers.
type OverlayController interface {
	SetOverlay(InteractiveCommand)
	ClearOverlay()
}
