package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/policy"
)

// Command is the unified abstraction for all slash commands.
type Command interface {
	Spec() CommandSpec
	Run(ctx *CommandContext, args []string) tea.Cmd
}

// InteractiveCommand extends Command with modal keyboard interception
// and custom rendering. When Active() returns true the TUI routes all
// keyboard events through HandleKey and replaces the input area with View.
type InteractiveCommand interface {
	Command
	Active() bool
	HandleKey(msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	View(width int) string
	Dismiss()
}

// CommandSpec describes command metadata.
type CommandSpec struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
	Risk        policy.CommandRisk
	NeedsIdle   bool
	Hidden      bool
}

// CommandContext provides runtime dependencies to commands.
type CommandContext struct {
	App *App
}

// SimpleCommand wraps a plain function as a Command.
type SimpleCommand struct {
	spec CommandSpec
	run  func(ctx *CommandContext, args []string) tea.Cmd
}

// NewSimple creates a SimpleCommand.
func NewSimple(spec CommandSpec, run func(*CommandContext, []string) tea.Cmd) *SimpleCommand {
	return &SimpleCommand{spec: spec, run: run}
}

func (c *SimpleCommand) Spec() CommandSpec                          { return c.spec }
func (c *SimpleCommand) Run(ctx *CommandContext, args []string) tea.Cmd { return c.run(ctx, args) }

// Registry manages command registration, lookup by name/alias, and the
// active interactive overlay.
type Registry struct {
	commands map[string]Command // name + alias → Command
	ordered  []Command          // registration order (for /help)
	overlay  InteractiveCommand // active interactive command (nil = none)
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]Command),
	}
}

// Register adds a command and its aliases to the registry.
func (r *Registry) Register(cmd Command) {
	spec := cmd.Spec()
	r.commands[strings.ToLower(spec.Name)] = cmd
	for _, alias := range spec.Aliases {
		r.commands[strings.ToLower(alias)] = cmd
	}
	r.ordered = append(r.ordered, cmd)
}

// Lookup finds a command by name or alias (case-insensitive).
func (r *Registry) Lookup(name string) (Command, bool) {
	cmd, ok := r.commands[strings.ToLower(name)]
	return cmd, ok
}

// All returns all registered commands in registration order.
func (r *Registry) All() []Command {
	return r.ordered
}

// Overlay returns the currently active interactive command, or nil.
func (r *Registry) Overlay() InteractiveCommand {
	return r.overlay
}

// SetOverlay sets the active interactive overlay.
func (r *Registry) SetOverlay(ic InteractiveCommand) {
	r.overlay = ic
}

// ClearOverlay dismisses and clears the active overlay.
func (r *Registry) ClearOverlay() {
	if r.overlay != nil {
		r.overlay.Dismiss()
	}
	r.overlay = nil
}
