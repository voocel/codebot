package ui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/policy"
)

type CommandKind string

const (
	CommandKindBuiltin CommandKind = "builtin"
	CommandKindCustom  CommandKind = "custom"
	CommandKindSkill   CommandKind = "skill"
)

var commandKindOrder = map[CommandKind]int{
	CommandKindBuiltin: 0,
	CommandKindCustom:  1,
	CommandKindSkill:   2,
}

// CommandInvocation is the parsed slash-command input passed to command handlers.
type CommandInvocation struct {
	Input   string
	Name    string
	RawArgs string
	Args    []string
}

// Command is the unified abstraction for all slash commands.
type Command interface {
	Spec() CommandSpec
	Run(ctx *CommandContext, inv CommandInvocation) tea.Cmd
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

// CommandLoader discovers a family of commands and contributes them to the registry.
type CommandLoader interface {
	Load(app *App) []Command
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
	Kind        CommandKind
	Source      string
}

// CommandContext provides runtime dependencies to commands.
type CommandContext struct {
	App *App
}

// SimpleCommand wraps a plain function as a Command.
type SimpleCommand struct {
	spec CommandSpec
	run  func(ctx *CommandContext, inv CommandInvocation) tea.Cmd
}

// NewSimple creates a SimpleCommand.
func NewSimple(spec CommandSpec, run func(*CommandContext, CommandInvocation) tea.Cmd) *SimpleCommand {
	return &SimpleCommand{spec: spec, run: run}
}

func (c *SimpleCommand) Spec() CommandSpec { return c.spec }
func (c *SimpleCommand) Run(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
	return c.run(ctx, inv)
}

// Registry manages command registration, lookup by name/alias, and the
// active interactive overlay.
type Registry struct {
	aliases       map[string]Command  // name + alias -> command
	entries       map[string]Command  // canonical name -> command
	ordered       []string            // canonical names, registration order
	activeAliases map[string][]string // canonical name -> effective aliases
	overlay       InteractiveCommand  // active interactive command (nil = none)
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		aliases:       make(map[string]Command),
		entries:       make(map[string]Command),
		activeAliases: make(map[string][]string),
	}
}

// Register adds or replaces a command and its aliases in the registry.
func (r *Registry) Register(cmd Command) {
	spec := cmd.Spec()
	canonical := strings.ToLower(spec.Name)

	if existing, ok := r.entries[canonical]; ok {
		r.unregister(existing)
	} else {
		r.ordered = append(r.ordered, canonical)
	}

	r.releaseAlias(canonical)
	r.entries[canonical] = cmd
	r.aliases[canonical] = cmd
	for _, alias := range spec.Aliases {
		alias = strings.ToLower(alias)
		if alias == "" || alias == canonical {
			continue
		}
		if _, reserved := r.entries[alias]; reserved {
			continue
		}
		r.releaseAlias(alias)
		r.aliases[alias] = cmd
		r.activeAliases[canonical] = append(r.activeAliases[canonical], alias)
	}
}

// RegisterAll adds a batch of commands to the registry.
func (r *Registry) RegisterAll(commands []Command) {
	for _, cmd := range commands {
		r.Register(cmd)
	}
}

func (r *Registry) unregister(cmd Command) {
	canonical := strings.ToLower(cmd.Spec().Name)
	delete(r.aliases, canonical)
	for _, alias := range r.activeAliases[canonical] {
		delete(r.aliases, alias)
	}
	delete(r.activeAliases, canonical)
}

func (r *Registry) releaseAlias(alias string) {
	prev, ok := r.aliases[alias]
	if !ok {
		return
	}
	prevCanonical := strings.ToLower(prev.Spec().Name)
	if prevCanonical == alias {
		return
	}
	r.activeAliases[prevCanonical] = removeAlias(r.activeAliases[prevCanonical], alias)
	delete(r.aliases, alias)
}

func removeAlias(aliases []string, target string) []string {
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if alias != target {
			out = append(out, alias)
		}
	}
	return out
}

// EffectiveSpec returns the command metadata with only active aliases included.
func (r *Registry) EffectiveSpec(cmd Command) CommandSpec {
	spec := cmd.Spec()
	canonical := strings.ToLower(spec.Name)
	spec.Aliases = append([]string(nil), r.activeAliases[canonical]...)
	return spec
}

// Lookup finds a command by name or alias (case-insensitive).
func (r *Registry) Lookup(name string) (Command, bool) {
	cmd, ok := r.aliases[strings.ToLower(name)]
	return cmd, ok
}

// All returns all registered commands sorted by kind and then by name.
func (r *Registry) All() []Command {
	commands := make([]Command, 0, len(r.entries))
	for _, canonical := range r.ordered {
		if cmd, ok := r.entries[canonical]; ok {
			commands = append(commands, cmd)
		}
	}

	slices.SortFunc(commands, func(a, b Command) int {
		specA := a.Spec()
		specB := b.Spec()
		if rank := compareCommandKinds(specA.Kind, specB.Kind); rank != 0 {
			return rank
		}
		return strings.Compare(specA.Name, specB.Name)
	})
	return commands
}

func compareCommandKinds(a, b CommandKind) int {
	return commandKindOrder[a] - commandKindOrder[b]
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
