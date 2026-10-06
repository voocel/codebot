package commands

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

func plugins(a *app.App) Command {
	return Command{
		Name:        "plugins",
		Args:        "[browse | add <source or name@marketplace> [--project] | update [name] | remove <name> | marketplace add|remove <source>]",
		Description: "List, browse, add, update or remove plugins",
		Idle:        true,
		Run: func(arg string) tea.Cmd {
			sub, rest, _ := strings.Cut(arg, " ")
			rest = strings.TrimSpace(rest)
			switch sub {
			case "":
				return pluginList(a)
			case "browse":
				return browse(a)
			case "add":
				words := strings.Fields(rest)
				project := slices.Contains(words, "--project")
				source := strings.Join(slices.DeleteFunc(words, func(w string) bool { return w == "--project" }), " ")
				if source == "" {
					return fail("Usage: /plugins add <git repository, path or name@marketplace> [--project]")
				}
				return addPlugin(a, source, project)
			case "update":
				return updatePlugins(a, rest)
			case "remove":
				if rest == "" {
					return fail("Usage: /plugins remove <name>")
				}
				return reloaded(func(ctx context.Context) (app.ReloadReport, error) { return a.RemovePlugin(ctx, rest) }, "Removed "+rest)
			case "marketplace":
				verb, source, _ := strings.Cut(rest, " ")
				switch source = strings.TrimSpace(source); {
				case source == "":
				case verb == "add":
					return addMarketplace(a, source)
				case verb == "remove":
					return func() tea.Msg {
						if err := a.RemoveMarketplace(source); err != nil {
							return transcript.Fail(err.Error())
						}
						return transcript.Note("Removed the marketplace " + source)
					}
				}
				return fail("Usage: /plugins marketplace add|remove <git repository or path>")
			}
			return fail("Unknown /plugins " + sub + ": try /plugins browse, add, update, remove or marketplace.")
		},
	}
}

// reloaded runs change off the TUI's goroutine and notes done once it has,
// with what the reload fetched and connected.
func reloaded(change func(context.Context) (app.ReloadReport, error), done string) tea.Cmd {
	return func() tea.Msg {
		r, err := change(context.Background())
		if err != nil {
			return transcript.Fail(err.Error())
		}
		return transcript.Note(done + connected(r))
	}
}

// connected sums up what a reload fetched and connected, "" for nothing.
func connected(r app.ReloadReport) string {
	var b strings.Builder
	if len(r.Fetched) > 0 {
		b.WriteString(" · fetched " + strings.Join(r.Fetched, ", "))
	}
	for _, e := range r.FetchErrors {
		b.WriteString(" · could not " + e)
	}
	if r.MCP.Servers > 0 {
		fmt.Fprintf(&b, " · %d MCP tools (%d servers connected, %d failed)", r.MCP.Tools, r.MCP.Connected, len(r.MCP.Errors))
	}
	return b.String()
}

// pluginKey identifies a declared plugin: by the settings declaring it, and
// its source there.
type pluginKey struct{ scope, source string }

func keyOf(pl app.Plugin) pluginKey { return pluginKey{string(pl.Scope), pl.Source} }

// findPlugin returns the plugin k identifies, as it stands now.
func findPlugin(a *app.App, k pluginKey) (app.Plugin, bool) {
	for _, pl := range a.Plugins() {
		if keyOf(pl) == k {
			return pl, true
		}
	}
	return app.Plugin{}, false
}

func pluginList(a *app.App) tea.Cmd {
	items := func() []panel.Item {
		var out []panel.Item
		for _, pl := range a.Plugins() {
			out = append(out, panel.Item{Group: string(pl.Scope), Title: pluginTitle(pl), Detail: pluginDetail(pl), Value: keyOf(pl)})
		}
		return out
	}
	first := items()
	if len(first) == 0 {
		return note("No plugins. /plugins browse lists those of the marketplaces, /plugins add <git repository or path> adds one; see the README.")
	}
	return show(&panel.List{
		Title:  "Plugins",
		Items:  first,
		Reload: items,
		Hint:   theme.Hint("↑↓", "select", "enter", "details", "space", "on/off here", "esc", "close"),
		Select: func(it panel.Item) tea.Cmd {
			if pl, ok := findPlugin(a, it.Value.(pluginKey)); ok {
				return show(&panel.Text{Title: it.Title, Tabs: []panel.Tab{{Body: func(w int) []string { return info(pluginRows(pl), w) }}}})
			}
			return nil
		},
		Keys: func(k string, it *panel.Item) (tea.Cmd, bool, bool) {
			if k != "space" || it == nil {
				return nil, false, false
			}
			selected := it.Value.(pluginKey)
			return func() tea.Msg {
				pl, ok := findPlugin(a, selected)
				switch {
				case !ok:
					return nil
				case pl.State != app.PluginOn && pl.State != app.PluginOff:
					return transcript.Fail(pluginTitle(pl) + " is " + string(pl.State) + ": it cannot be turned on or off")
				}
				on := pl.State == app.PluginOff
				if _, err := a.SetPluginEnabled(context.Background(), pl.Name, on); err != nil {
					return transcript.Fail(err.Error())
				}
				if on {
					return transcript.Note("Turned " + pl.Name + " on here")
				}
				return transcript.Note("Turned " + pl.Name + " off here")
			}, true, false
		},
	})
}

func pluginTitle(pl app.Plugin) string {
	if pl.Plugin != nil && pl.Version != "" {
		return pl.Name + " " + pl.Version
	}
	return pl.Title()
}

// pluginDetail sums a plugin up for its row: what it brings, where it is
// from, and where it stands.
func pluginDetail(pl app.Plugin) string {
	var parts []string
	if pl.Plugin != nil {
		parts = append(parts, brings(pl))
		if pl.Commit != "" {
			parts = append(parts, pl.Source+" @ "+short(pl.Commit))
		} else {
			parts = append(parts, pl.Source)
		}
	}
	state := string(pl.State)
	switch pl.State {
	case app.PluginShadowed:
		state = "another " + pl.Name + " is on in its stead"
	case app.PluginHeld:
		state = "waits for /trust"
	case app.PluginMissing:
		if pl.Commit == "" && pl.Scope == "user" {
			state = "not installed · /plugins add " + pl.Source
		} else {
			state = "not fetched · /reload fetches it"
		}
	case app.PluginBroken:
		state = "broken: " + pl.Err.Error()
	}
	return strings.Join(append(parts, state), " · ")
}

func short(commit string) string { return commit[:min(7, len(commit))] }

// brings counts what a plugin read brings: skills, agents, MCP servers and
// hooks, those it has none of left out but skills and MCP.
func brings(pl app.Plugin) string {
	hooks := 0
	for _, hs := range pl.Hooks {
		hooks += len(hs)
	}
	parts := []string{fmt.Sprintf("%d %s", len(pl.Skills), plural(len(pl.Skills), "skill"))}
	if len(pl.Agents) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(pl.Agents), plural(len(pl.Agents), "agent")))
	}
	parts = append(parts, fmt.Sprintf("%d MCP", len(pl.MCP)))
	if hooks > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", hooks, plural(hooks, "hook")))
	}
	return strings.Join(parts, " · ")
}

func pluginRows(pl app.Plugin) [][2]string {
	rows := [][2]string{{"Source", pl.Source}, {"Declared by", string(pl.Scope) + " settings"}, {"State", string(pl.State)}}
	if pl.Err != nil {
		rows = append(rows, [2]string{"Problem", pl.Err.Error()})
	}
	if pl.Commit != "" {
		rows = append(rows, [2]string{"Commit", pl.Commit})
	}
	if pl.Plugin == nil {
		return rows
	}
	rows = append(rows, [2]string{"Directory", transcript.HomePath(pl.Root)})
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
	if len(pl.Hooks) > 0 {
		rows = append(rows, [2]string{"Hooks", ""})
		for _, event := range slices.Sorted(maps.Keys(pl.Hooks)) {
			for _, h := range pl.Hooks[event] {
				rows = append(rows, [2]string{event, cmp.Or(h.Command, h.Prompt, h.URL)})
			}
		}
	}
	if len(pl.MCP) > 0 {
		rows = append(rows, [2]string{"MCP servers", ""})
		for _, name := range slices.Sorted(maps.Keys(pl.MCP)) {
			srv := pl.MCP[name]
			what := srv.URL
			if what == "" {
				what = strings.Join(append([]string{srv.Command}, srv.Args...), " ")
			}
			rows = append(rows, [2]string{pl.Name + "_" + name, what})
		}
	}
	return rows
}

func addPlugin(a *app.App, source string, project bool) tea.Cmd {
	fetch := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		o, err := a.OfferPlugin(ctx, source, project)
		if err != nil {
			return transcript.Fail("Could not add " + source + ": " + err.Error())
		}
		where := o.Source
		if o.Commit != "" {
			where += " @ " + short(o.Commit)
		}
		lead := where + " brings " + brings(app.Plugin{Plugin: o.Plugin})
		if len(o.Surface) > 0 {
			lead += ", and would run:"
		}
		var cells []tea.Cmd
		for _, p := range o.Problems {
			cells = append(cells, fail(p.Error()))
		}
		dismiss := func() tea.Cmd { return note("Did not add " + o.Name) }
		add := func() tea.Cmd {
			return func() tea.Msg {
				r, err := a.AddPlugin(context.Background(), o)
				if err != nil {
					return transcript.Fail(err.Error())
				}
				return transcript.Note(added(a, o) + connected(r))
			}
		}
		title := "Add " + o.Name + "?"
		if o.Version != "" {
			title = "Add " + o.Name + " " + o.Version + "?"
		}
		cells = append(cells, show(panel.NewConsent(new(int), title, lead, o.Surface, []panel.Choice{
			{Label: "Add", Pick: add},
			{Label: "Cancel", Pick: dismiss},
		}, dismiss)))
		return tea.BatchMsg(cells)
	}
	return tea.Sequence(note("Fetching "+source+"…"), fetch)
}

// added tells how the plugin offered stands once added.
func added(a *app.App, o *app.PluginOffer) string {
	for _, pl := range a.Plugins() {
		if pl.Plugin == nil || pl.Name != o.Name {
			continue
		}
		switch pl.State {
		case app.PluginOn:
			return "Added " + o.Name + " · " + brings(pl)
		case app.PluginHeld:
			return "Added " + o.Name + " to the project · it waits for /trust"
		}
		return "Added " + o.Name + " · " + pluginDetail(pl)
	}
	return "Added " + o.Source
}

// browse lists the plugins the marketplaces list, to add one: for the
// user with enter, to the project with tab.
func browse(a *app.App) tea.Cmd {
	read := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		ms, errs := a.Marketplaces(ctx)
		var cells []tea.Cmd
		for _, err := range errs {
			cells = append(cells, fail(err.Error()))
		}
		var items []panel.Item
		for _, m := range ms {
			for _, l := range m.Plugins {
				items = append(items, panel.Item{Group: m.Title, Title: l.Name, Detail: listingDetail(a, l), Value: l})
			}
		}
		if len(items) == 0 {
			return tea.BatchMsg(append(cells, note("No marketplace lists a plugin. /plugins marketplace add <git repository or path> adds one; see the README.")))
		}
		pick := func(l app.Listing, project bool) tea.Cmd {
			if l.Source == "" {
				return fail("Cannot add " + l.Name + ": " + l.Unsupported)
			}
			return addPlugin(a, l.Source, project)
		}
		return tea.BatchMsg(append(cells, show(&panel.List{
			Title:  "Marketplaces",
			Items:  items,
			Filter: true,
			Hint:   theme.Hint("↑↓", "select", "enter", "add", "tab", "add to the project", "esc", "close"),
			Select: func(it panel.Item) tea.Cmd { return pick(it.Value.(app.Listing), false) },
			Keys: func(k string, it *panel.Item) (tea.Cmd, bool, bool) {
				if k != "tab" || it == nil {
					return nil, false, false
				}
				return pick(it.Value.(app.Listing), true), true, true
			},
		})))
	}
	return tea.Sequence(note("Reading the marketplaces…"), read)
}

// listingDetail sums a listed plugin up for its row.
func listingDetail(a *app.App, l app.Listing) string {
	var parts []string
	for _, p := range []string{l.Description, l.Category} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	switch {
	case l.Source == "":
		parts = append(parts, "cannot be added: "+l.Unsupported)
	case a.Declares(l.Source):
		parts = append(parts, "added")
	}
	return strings.Join(parts, " · ")
}

func addMarketplace(a *app.App, source string) tea.Cmd {
	read := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		m, problems, err := a.AddMarketplace(ctx, source)
		if err != nil {
			return transcript.Fail("Could not add the marketplace " + source + ": " + err.Error())
		}
		var cells []tea.Cmd
		for _, p := range problems {
			cells = append(cells, fail(p.Error()))
		}
		return tea.BatchMsg(append(cells, note(fmt.Sprintf("Added the marketplace %s · it lists %d %s · /plugins browse", m.Title, len(m.Plugins), plural(len(m.Plugins), "plugin")))))
	}
	return tea.Sequence(note("Reading "+source+"…"), read)
}

func updatePlugins(a *app.App, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
			case u.Commit == pl.Commit && u.Applied:
				cells = append(cells, note("Fetched "+title+" at "+short(u.Commit)+" again"))
			case u.Commit == pl.Commit:
				cells = append(cells, note(title+" is up to date"))
			case u.Applied:
				cells = append(cells, note("Updated "+title+" to "+short(u.Commit)))
			default:
				keep := func() tea.Cmd { return note("Kept " + title + " at " + short(pl.Commit)) }
				apply := func() tea.Cmd {
					return reloaded(func(ctx context.Context) (app.ReloadReport, error) { return a.ApplyUpdate(ctx, u) }, "Updated "+title+" to "+short(u.Commit))
				}
				lead := title + " " + short(pl.Commit) + " → " + short(u.Commit) + " would also run:"
				cells = append(cells, show(panel.NewConsent(new(int), "Update "+title+"?", lead, u.Added, []panel.Choice{
					{Label: "Update", Pick: apply},
					{Label: "Keep " + short(pl.Commit), Pick: keep},
				}, keep)))
			}
		}
		if err != nil {
			cells = append(cells, fail(err.Error()))
		}
		return tea.BatchMsg(cells)
	}
}
