package commands

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// permissions lists what runs without asking beyond the mode: the approvals
// remembered with "don't ask again", which x forgets, and the rules of the
// settings, which are edited there.
func permissions(a *app.App) Command {
	return Command{Name: "permissions", Description: "See and forget what runs without asking", Run: func(string) tea.Cmd {
		items := func() []panel.Item {
			var out []panel.Item
			for _, ap := range a.Approvals() {
				group := "Tools you allowed"
				if ap.Command {
					group = "Commands you allowed"
				}
				detail := transcript.Ago(time.Since(ap.Added))
				if ap.Example != "" {
					detail += " · " + app.Printable(firstLine(ap.Example))
				}
				out = append(out, panel.Item{Group: group, Title: app.Printable(ap.Name), Detail: detail, Value: ap.Key})
			}
			p := a.Settings().Permissions
			for _, rule := range p.Allow {
				out = append(out, panel.Item{Group: "Allowed by the settings", Title: app.Printable(rule)})
			}
			for _, rule := range p.Deny {
				out = append(out, panel.Item{Group: "Denied by the settings", Title: app.Printable(rule)})
			}
			return out
		}
		first := items()
		if len(first) == 0 {
			return note("Nothing runs without asking beyond the " + string(a.Mode()) + " mode: no approval is remembered and the settings hold no rules.")
		}
		var l *panel.List
		l = &panel.List{
			Title: "Permissions · " + string(a.Mode()) + " mode",
			Items: first,
			Hint:  theme.Hint("↑↓", "select", "x", "forget", "esc", "close"),
			Keys: func(k string, it *panel.Item) (tea.Cmd, bool, bool) {
				if k != "x" || it == nil {
					return nil, false, false
				}
				key, ok := it.Value.(string)
				if !ok {
					return note("Rules of the settings are edited in the settings file."), true, false
				}
				if err := a.Forget(key); err != nil {
					return fail("Could not forget " + it.Title + ": " + err.Error()), true, false
				}
				l.Items = items()
				return note("Forgot " + it.Title + ": it is asked about again."), true, len(l.Items) == 0
			},
		}
		return show(l)
	}}
}
