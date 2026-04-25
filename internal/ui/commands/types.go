// Package commands holds the slash-command implementations and the abstractions
// required to register them with the host UI.
//
// Design: each command's New* constructor explicitly declares the dependencies
// it needs (Session, Catalog, callbacks, ...). There is no shared "Deps"
// interface — the registration site doubles as the dependency graph.
package commands

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Kind classifies how a command was contributed.
type Kind string

const (
	KindBuiltin Kind = "builtin"
	KindCustom  Kind = "custom"
	KindSkill   Kind = "skill"
)

// Spec describes command metadata used by the registry, palette, and help command.
type Spec struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
	Category    string
	NeedsIdle   bool
	Hidden      bool
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

// ModalOverlay is an optional interface for overlays that replace the input area.
type ModalOverlay interface {
	IsModal() bool
}

// Registry is the surface a command needs from the host to enumerate its
// peers (e.g. /help listing all commands) and to install/dismiss the active
// modal overlay. Implemented by ui.Registry.
type Registry interface {
	All() []Command
	EffectiveSpec(cmd Command) Spec
	SetOverlay(InteractiveCommand)
	ClearOverlay()
}
