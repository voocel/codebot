package commands

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui"
)

var kindOrder = map[Kind]int{
	KindBuiltin: 0,
	KindSkill:   1,
}

// Table holds the slash commands — the built-in ones and one per
// user-invocable skill — and the interactive command on screen. It is the
// TUI's tui.Commands.
type Table struct {
	app     *app.App
	version string

	aliases       map[string]Command  // name and alias → command
	entries       map[string]Command  // canonical name → command
	ordered       []string            // canonical names, registration order
	activeAliases map[string][]string // canonical name → effective aliases
	overlay       InteractiveCommand  // nil when none is open
}

var _ tui.Commands = (*Table)(nil)

// New builds the commands for a.
func New(a *app.App, version string) *Table {
	t := &Table{app: a, version: version}
	t.Rebuild()
	return t
}

// Rebuild registers the commands again, picking up reloaded skills. The open
// overlay stays.
func (t *Table) Rebuild() {
	t.aliases = make(map[string]Command)
	t.entries = make(map[string]Command)
	t.activeAliases = make(map[string][]string)
	t.ordered = t.ordered[:0]
	for _, sk := range t.app.Current().Skills() {
		if !sk.DisableUserInvocation {
			t.Register(&skillCommand{app: t.app, skill: sk})
		}
	}
	for _, cmd := range t.builtins() {
		t.Register(cmd)
	}
}

// builtins lists the built-in commands. Each constructor's arguments are
// what the command depends on.
func (t *Table) builtins() []Command {
	a := t.app
	return []Command{
		Help(t),
		Model(a, t),
		Compact(a),
		&StatusCommand{app: a, overlay: t, version: t.version},
		Context(a, t),
		NewSession(a),
		Resume(a, t),
		Tasks(a, t),
		Btw(a, t),
		Settings(a, t),
		MCP(a),
		&PluginsCommand{app: a, table: t},
		Copy(a),
		Reload(a, t),
		Memory(a),
		Undo(a),
		Redo(a),
		Diff(a, t),
		Worktree(a),
		Exit(),
	}
}

// Run runs a "/name args" line.
func (t *Table) Run(line string) tea.Cmd {
	inv, ok := ParseInvocation(line)
	if !ok {
		return nil
	}
	cmd, ok := t.Lookup(inv.Name)
	if !ok {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			fmt.Sprintf("Unknown command: /%s. Type / to browse commands, or /help for the full list.", inv.Name)))
	}
	if spec := cmd.Spec(); spec.NeedsIdle && t.app.Current().Status().Running {
		return tui.SendCommandResult(tui.ErrorStyle.Render("/" + spec.Name + " needs an idle agent; press Esc to abort the current run."))
	}
	return cmd.Run(inv)
}

// Register adds or replaces a command and its aliases.
func (t *Table) Register(cmd Command) {
	spec := cmd.Spec()
	canonical := strings.ToLower(spec.Name)

	if existing, ok := t.entries[canonical]; ok {
		t.unregister(existing)
	} else {
		t.ordered = append(t.ordered, canonical)
	}

	t.releaseAlias(canonical)
	t.entries[canonical] = cmd
	t.aliases[canonical] = cmd
	for _, alias := range spec.Aliases {
		alias = strings.ToLower(alias)
		if alias == "" || alias == canonical {
			continue
		}
		if _, reserved := t.entries[alias]; reserved {
			continue
		}
		t.releaseAlias(alias)
		t.aliases[alias] = cmd
		t.activeAliases[canonical] = append(t.activeAliases[canonical], alias)
	}
}

func (t *Table) unregister(cmd Command) {
	canonical := strings.ToLower(cmd.Spec().Name)
	delete(t.aliases, canonical)
	for _, alias := range t.activeAliases[canonical] {
		delete(t.aliases, alias)
	}
	delete(t.activeAliases, canonical)
}

func (t *Table) releaseAlias(alias string) {
	prev, ok := t.aliases[alias]
	if !ok {
		return
	}
	prevCanonical := strings.ToLower(prev.Spec().Name)
	if prevCanonical == alias {
		return
	}
	t.activeAliases[prevCanonical] = slices.DeleteFunc(t.activeAliases[prevCanonical], func(a string) bool { return a == alias })
	delete(t.aliases, alias)
}

// EffectiveSpec returns the command's metadata with only its active aliases.
func (t *Table) EffectiveSpec(cmd Command) Spec {
	spec := cmd.Spec()
	spec.Aliases = slices.Clone(t.activeAliases[strings.ToLower(spec.Name)])
	return spec
}

// Lookup finds a command by name or alias, ignoring case.
func (t *Table) Lookup(name string) (Command, bool) {
	cmd, ok := t.aliases[strings.ToLower(name)]
	return cmd, ok
}

// All returns the commands sorted by kind, then name.
func (t *Table) All() []Command {
	cmds := make([]Command, 0, len(t.entries))
	for _, canonical := range t.ordered {
		if cmd, ok := t.entries[canonical]; ok {
			cmds = append(cmds, cmd)
		}
	}
	slices.SortFunc(cmds, func(a, b Command) int {
		sa, sb := a.Spec(), b.Spec()
		if d := kindOrder[sa.Kind] - kindOrder[sb.Kind]; d != 0 {
			return d
		}
		return strings.Compare(sa.Name, sb.Name)
	})
	return cmds
}

// Overlay is the interactive command on screen, or nil.
func (t *Table) Overlay() *tui.OverlayState {
	ov := t.overlay
	if ov == nil || !ov.Active() {
		return nil
	}
	return &tui.OverlayState{HandleKey: ov.HandleKey, View: ov.View}
}

// SetOverlay opens an interactive command.
func (t *Table) SetOverlay(ic InteractiveCommand) { t.overlay = ic }

// ClearOverlay dismisses the open interactive command.
func (t *Table) ClearOverlay() {
	if t.overlay != nil {
		t.overlay.Dismiss()
	}
	t.overlay = nil
}

// Complete lists the commands matching prefix, best match first.
func (t *Table) Complete(prefix string) []tui.CompletionItem {
	query := strings.TrimSpace(strings.ToLower(prefix))
	type match struct {
		item  tui.CompletionItem
		score int
	}
	var matches []match
	for _, cmd := range t.All() {
		spec := t.EffectiveSpec(cmd)
		if score, ok := paletteScore(spec, query); ok {
			matches = append(matches, match{item: paletteItem(spec), score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		ki, kj := kindOrder[Kind(matches[i].item.Kind)], kindOrder[Kind(matches[j].item.Kind)]
		if ki != kj {
			return ki < kj
		}
		return matches[i].item.Name < matches[j].item.Name
	})
	items := make([]tui.CompletionItem, len(matches))
	for i, m := range matches {
		items[i] = m.item
	}
	return items
}

func paletteItem(spec Spec) tui.CompletionItem {
	item := tui.CompletionItem{
		Name:        spec.Name,
		Description: spec.Description,
		Kind:        string(spec.Kind),
		Aliases:     spec.Aliases,
		// A command without arguments runs on Enter.
		AutoExecute: strings.TrimSpace(spec.Usage) == "" || strings.TrimSpace(spec.Usage) == "/"+spec.Name,
	}
	if item.Description == "" {
		item.Description = "(no description)"
	}
	return item
}

func paletteScore(spec Spec, query string) (int, bool) {
	if query == "" {
		return 100, true
	}
	best := scoreField(strings.ToLower(spec.Name), query, 1200, 950, 700)
	for _, alias := range spec.Aliases {
		best = max(best, scoreField(strings.ToLower(alias), query, 1100, 900, 650))
	}
	return best, best > 0
}

func scoreField(field, query string, exact, prefix, contains int) int {
	switch {
	case field == "":
		return 0
	case field == query:
		return exact
	case strings.HasPrefix(field, query):
		return prefix - min(len(field)-len(query), 40)
	case strings.Contains(field, query):
		return contains
	default:
		return 0
	}
}

// skillCommand runs a user-invocable skill.
type skillCommand struct {
	app   *app.App
	skill app.Skill
}

func (c *skillCommand) Spec() Spec {
	usage := "/" + c.skill.Name + " [args]"
	if c.skill.ArgumentHint != "" {
		usage = "/" + c.skill.Name + " " + c.skill.ArgumentHint
	}
	return Spec{
		Name:        c.skill.Name,
		Usage:       usage,
		Description: c.skill.Description,
		Kind:        KindSkill,
		Source:      c.skill.Source,
	}
}

// Run sends an inline skill to the agent; a forked one runs in a sub-agent
// whose output is shown.
func (c *skillCommand) Run(inv Invocation) tea.Cmd {
	conv := c.app.Current()
	return func() tea.Msg {
		out, err := conv.InvokeSkill(context.Background(), c.skill.Name, inv.RawArgs)
		switch {
		case err != nil:
			return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("Skill failed: " + err.Error())}
		case out != "":
			return tui.CommandResultMsg{Text: tui.CommandStyle.Render(out)}
		}
		return nil
	}
}
