package ui

import (
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/ui/commands"
)

var commandKindOrder = map[commands.Kind]int{
	commands.KindBuiltin: 0,
	commands.KindCustom:  1,
	commands.KindSkill:   2,
}

// Compile-time assertion that *Registry satisfies commands.Registry. Without
// this, a method-signature drift in the interface would only surface when a
// command tries to call the missing method, far from the root cause.
var _ commands.Registry = (*Registry)(nil)

// Registry manages command registration, lookup by name/alias, and the
// active interactive overlay.
type Registry struct {
	aliases       map[string]commands.Command // name + alias -> command
	entries       map[string]commands.Command // canonical name -> command
	ordered       []string                    // canonical names, registration order
	activeAliases map[string][]string         // canonical name -> effective aliases
	overlay       commands.InteractiveCommand // active interactive command (nil = none)
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		aliases:       make(map[string]commands.Command),
		entries:       make(map[string]commands.Command),
		activeAliases: make(map[string][]string),
	}
}

// Reset clears all registered commands and aliases. The active overlay is
// preserved because rebuilds (e.g. on /reload) should not dismiss an open
// modal that the user is interacting with.
func (r *Registry) Reset() {
	r.aliases = make(map[string]commands.Command)
	r.entries = make(map[string]commands.Command)
	r.activeAliases = make(map[string][]string)
	r.ordered = r.ordered[:0]
}

// Register adds or replaces a command and its aliases in the registry.
func (r *Registry) Register(cmd commands.Command) {
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
func (r *Registry) RegisterAll(cmds []commands.Command) {
	for _, cmd := range cmds {
		r.Register(cmd)
	}
}

func (r *Registry) unregister(cmd commands.Command) {
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
func (r *Registry) EffectiveSpec(cmd commands.Command) commands.Spec {
	spec := cmd.Spec()
	canonical := strings.ToLower(spec.Name)
	spec.Aliases = append([]string(nil), r.activeAliases[canonical]...)
	return spec
}

// Lookup finds a command by name or alias (case-insensitive).
func (r *Registry) Lookup(name string) (commands.Command, bool) {
	cmd, ok := r.aliases[strings.ToLower(name)]
	return cmd, ok
}

// All returns all registered commands sorted by kind and then by name.
func (r *Registry) All() []commands.Command {
	cmds := make([]commands.Command, 0, len(r.entries))
	for _, canonical := range r.ordered {
		if cmd, ok := r.entries[canonical]; ok {
			cmds = append(cmds, cmd)
		}
	}

	slices.SortFunc(cmds, func(a, b commands.Command) int {
		specA := a.Spec()
		specB := b.Spec()
		if rank := compareCommandKinds(specA.Kind, specB.Kind); rank != 0 {
			return rank
		}
		return strings.Compare(specA.Name, specB.Name)
	})
	return cmds
}

func compareCommandKinds(a, b commands.Kind) int {
	return commandKindOrder[a] - commandKindOrder[b]
}

// Overlay returns the currently active interactive command, or nil.
func (r *Registry) Overlay() commands.InteractiveCommand {
	return r.overlay
}

// SetOverlay sets the active interactive overlay.
func (r *Registry) SetOverlay(ic commands.InteractiveCommand) {
	r.overlay = ic
}

// ClearOverlay dismisses and clears the active overlay.
func (r *Registry) ClearOverlay() {
	if r.overlay != nil {
		r.overlay.Dismiss()
	}
	r.overlay = nil
}
