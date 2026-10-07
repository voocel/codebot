package commands

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// fetchTimeout stops a hanging git fetch.
const fetchTimeout = 5 * time.Minute

func plugins(a *app.App) Command {
	return Command{
		Name:        "plugins",
		Args:        "[add <source> [--project] | install | update [name] | remove <name> [--project]]",
		Description: "List, add, install, update or remove plugins",
		Idle:        true,
		Run: func(arg string) tea.Cmd {
			words := strings.Fields(arg)
			project := slices.Contains(words, "--project")
			words = slices.DeleteFunc(words, func(w string) bool { return w == "--project" })
			sub, rest := "", ""
			if len(words) > 0 {
				sub, rest = words[0], strings.Join(words[1:], " ")
			}
			switch sub {
			case "":
				return pluginList(a)
			case "add":
				if rest == "" {
					return fail("Usage: /plugins add <git repository or path> [--project]")
				}
				return addPlugin(a, rest, project)
			case "install":
				return installPlugins(a)
			case "update":
				return updatePlugins(a, rest)
			case "remove":
				if rest == "" {
					return fail("Usage: /plugins remove <name> [--project]")
				}
				return reloaded(func(ctx context.Context) (app.ReloadReport, error) { return a.RemovePlugin(ctx, rest, project) }, "Removed "+rest)
			}
			return fail("Unknown /plugins " + sub + ": try /plugins add, install, update or remove.")
		},
	}
}

func reloaded(change func(context.Context) (app.ReloadReport, error), done string) tea.Cmd {
	return func() tea.Msg {
		r, err := change(context.Background())
		if err != nil {
			return transcript.Fail(err.Error())
		}
		return transcript.Note(done + connected(r.MCP))
	}
}

func connected(r app.MCPReport) string {
	if r.Servers == 0 {
		return ""
	}
	s := fmt.Sprintf(" · %d MCP tools (%d servers connected, %d failed)", r.Tools, r.Connected, len(r.Errors))
	for _, name := range r.Login {
		s += " · " + name + " needs login: /mcp login " + name
	}
	return s
}

// PendingPlugins counts plugins that need installing or a consent decision.
func PendingPlugins(a *app.App) string {
	n := 0
	for _, pl := range a.Plugins() {
		if pl.State == app.PluginNotInstalled || pl.State == app.PluginNotCached || pl.State == app.PluginOn && len(pl.Ask()) > 0 {
			n++
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
		return "1 plugin waits for you · /plugins install"
	}
	return fmt.Sprintf("%d plugins wait for you · /plugins install", n)
}

type pluginKey struct{ scope, source string }

func keyOf(pl app.Plugin) pluginKey { return pluginKey{string(pl.Scope), pl.Source} }

func pluginList(a *app.App) tea.Cmd {
	items := func() []panel.Item {
		var out []panel.Item
		for _, pl := range a.Plugins() {
			out = append(out, panel.Item{Group: string(pl.Scope), Title: app.Printable(pluginTitle(pl)), Detail: app.Printable(pluginDetail(pl)), Value: keyOf(pl)})
		}
		return out
	}
	first := items()
	if len(first) == 0 {
		return note("No plugins. /plugins add <git repository or path> adds one; see the README.")
	}
	return show(&panel.List{
		Title:  "Plugins",
		Items:  first,
		Reload: items,
		Hint:   theme.Hint("↑↓", "select", "enter", "details", "esc", "close"),
		Select: func(it panel.Item) tea.Cmd {
			plugins := a.Plugins()
			i := slices.IndexFunc(plugins, func(pl app.Plugin) bool { return keyOf(pl) == it.Value.(pluginKey) })
			if i < 0 {
				return nil
			}
			return show(&panel.Text{Title: it.Title, Tabs: []panel.Tab{{Body: func(w int) []string { return info(pluginRows(plugins[i]), w) }}}})
		},
	})
}

func pluginTitle(pl app.Plugin) string {
	if pl.Plugin != nil && pl.Version != "" {
		return pl.Name + " " + pl.Version
	}
	return pl.Title()
}

func pluginDetail(pl app.Plugin) string {
	var parts []string
	if pl.Plugin != nil {
		parts = append(parts, brings(pl.Plugin))
	}
	if pl.Commit != "" {
		parts = append(parts, pl.Source+" @ "+short(pl.Commit))
	} else {
		parts = append(parts, pl.Source)
	}
	return strings.Join(append(parts, pluginState(pl)), " · ")
}

func pluginState(pl app.Plugin) string {
	switch pl.State {
	case app.PluginOn:
		if ask := pl.Ask(); len(ask) > 0 {
			return fmt.Sprintf("on · %d waiting for you · /plugins install", len(ask))
		}
	case app.PluginShadowed:
		return "another " + pl.Name + " is on in its stead"
	case app.PluginUntrusted:
		return "waits for /trust"
	case app.PluginNotInstalled, app.PluginNotCached:
		return string(pl.State) + " · /plugins install"
	case app.PluginBroken:
		return "broken: " + pl.Err.Error()
	}
	return string(pl.State)
}

func short(commit string) string { return commit[:min(7, len(commit))] }

func versioned(name, version string) string {
	if version == "" {
		return name
	}
	return name + " " + version
}

// brings always lists skills and MCP servers, and agents and hooks only
// when there are some.
func brings(p *app.PluginContent) string {
	hooks := 0
	for _, hs := range p.Hooks {
		hooks += len(hs)
	}
	parts := []string{fmt.Sprintf("%d %s", len(p.Skills), plural(len(p.Skills), "skill"))}
	if len(p.Agents) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(p.Agents), plural(len(p.Agents), "agent")))
	}
	parts = append(parts, fmt.Sprintf("%d MCP", len(p.MCP)))
	if hooks > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", hooks, plural(hooks, "hook")))
	}
	return strings.Join(parts, " · ")
}

func pluginRows(pl app.Plugin) [][2]string {
	rows := [][2]string{{"Source", pl.Source}, {"Declared by", string(pl.Scope)}, {"State", pluginState(pl)}}
	if pl.Commit != "" {
		rows = append(rows, [2]string{"Commit", pl.Commit})
	}
	if pl.Plugin == nil {
		return rows
	}
	rows = append(rows, [2]string{"Directory", transcript.HomePath(pl.Root)}, [2]string{"Data", transcript.HomePath(pl.Data)})
	if pl.Description != "" {
		rows = append(rows, [2]string{"About", pl.Description})
	}
	if len(pl.Skills) > 0 {
		rows = append(rows, [2]string{"Skills", ""})
		for _, s := range pl.Skills {
			rows = append(rows, [2]string{"/" + pl.Name + ":" + s.Name, s.Description})
		}
	}
	if len(pl.Agents) > 0 {
		rows = append(rows, [2]string{"Agents", ""})
		for _, d := range pl.Agents {
			rows = append(rows, [2]string{pl.Name + ":" + d.Name, d.Description})
		}
	}
	if len(pl.Surface) > 0 {
		rows = append(rows, [2]string{"Runs", ""})
		for _, it := range pl.Surface {
			detail := it.Detail
			switch {
			case pl.Declined.Has(it):
				detail += " · declined"
			case !pl.Agreed.Has(it):
				detail += " · waiting for you"
			}
			rows = append(rows, [2]string{panel.KindLabel(it.Kind), detail})
		}
	}
	return rows
}

// offerPanel asks about the plugin's undecided items. accept agrees to the
// checked items and declines the rest.
func offerPanel(a *app.App, o *app.PluginOffer, title, accept, done string) tea.Cmd {
	where := o.Source
	if o.Commit != "" {
		where += " @ " + short(o.Commit)
	}
	lead := where + " brings " + brings(o.Plugin)
	if len(o.New) > 0 {
		lead += ", and would run:"
	}
	var cells []tea.Cmd
	for _, p := range o.Problems {
		cells = append(cells, fail(p.Error()))
	}
	dismiss := func() tea.Cmd { return note("Left " + o.Name + " as it was") }
	pick := func(agreed app.Surface) tea.Cmd {
		return reloaded(func(ctx context.Context) (app.ReloadReport, error) { return a.AcceptPlugin(ctx, o, agreed) }, done)
	}
	cells = append(cells, show(panel.NewConsent(new(int), title+"?", lead, o.New, o.New, []panel.Choice{
		{Label: accept, Pick: pick},
		{Label: "Cancel", Pick: func(app.Surface) tea.Cmd { return dismiss() }},
	}, dismiss)))
	return tea.Batch(cells...)
}

func addPlugin(a *app.App, source string, project bool) tea.Cmd {
	fetch := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		o, err := a.OfferPlugin(ctx, source, project)
		if err != nil {
			return transcript.Fail("Could not add " + source + ": " + err.Error())
		}
		done := "Added " + o.Name
		if project {
			done += " to the project"
		}
		return offerPanel(a, o, "Add "+versioned(o.Name, o.Version), "Add", done)()
	}
	return tea.Sequence(note("Fetching "+source+"…"), fetch)
}

func installPlugins(a *app.App) tea.Cmd {
	install := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		offers, fetched, errs := a.InstallPlugins(ctx)
		var cells []tea.Cmd
		for _, err := range errs {
			cells = append(cells, fail("Could not install "+err.Error()))
		}
		if len(fetched) > 0 {
			cells = append(cells, note("Fetched "+strings.Join(fetched, ", ")+" at the commits you agreed to"))
		}
		for _, o := range offers {
			cells = append(cells, offerPanel(a, o, "Install "+versioned(o.Name, o.Version), "Install", "Installed "+o.Name))
		}
		if len(cells) == 0 {
			return transcript.Note("Every plugin is installed, all it runs agreed to")
		}
		return tea.BatchMsg(cells)
	}
	return tea.Sequence(note("Installing the plugins…"), install)
}

func updatePlugins(a *app.App, name string) tea.Cmd {
	update := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		updates, err := a.UpdatePlugins(ctx, name)
		if err != nil && len(updates) == 0 {
			return transcript.Fail(err.Error())
		}
		var cells []tea.Cmd
		for _, u := range updates {
			pl, title := u.Plugin, u.Plugin.Title()
			switch {
			case u.Err != nil:
				cells = append(cells, fail("Could not update "+title+": "+u.Err.Error()))
			case u.Commit == pl.Commit:
				cells = append(cells, note(title+" is up to date"))
			case u.Offer == nil:
				cells = append(cells, note("Updated "+title+" to "+short(u.Commit)))
			default:
				from, to := short(pl.Commit), short(u.Commit)
				if pl.Version != "" && u.Offer.Version != "" && pl.Version != u.Offer.Version {
					from, to = pl.Version, u.Offer.Version
				}
				cells = append(cells, offerPanel(a, u.Offer, "Update "+title+" "+from+" → "+to, "Update", "Updated "+title+" to "+short(u.Commit)))
			}
		}
		if err != nil {
			cells = append(cells, fail(err.Error()))
		}
		return tea.BatchMsg(cells)
	}
	return tea.Sequence(note("Fetching…"), update)
}
