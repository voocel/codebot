package commands

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// askKey marks the trust panel codebot shows on its own, at start or when
// the folder's surface grows; trustKey marks the one /trust shows.
var askKey, trustKey = new(int), new(int)

func IsAsk(p panel.Panel) bool {
	r, ok := p.(panel.Request)
	return ok && r.Key() == askKey
}

// TrustPanel asks about the undecided items, or the whole surface with all.
// Dismissing it changes nothing.
func TrustPanel(a *app.App, all bool) panel.Panel {
	t := a.Trust()
	folder := transcript.HomePath(t.Root)
	key, items, checked, lead := askKey, t.Ask(), t.Ask(), folder+" would turn on:"
	no := panel.Choice{Label: "Don't trust these · keep working without them", Pick: func(app.Surface) tea.Cmd {
		return decide(a, "Keeping them off; /trust changes that", func(ctx context.Context) (app.ReloadReport, error) {
			return a.SetTrust(ctx, items, nil)
		})
	}}
	switch {
	case all:
		key, items, checked = trustKey, t.Surface, t.Surface.Missing(t.Declined)
		no = panel.Choice{Label: "Don't trust this folder", Pick: func(app.Surface) tea.Cmd {
			return decide(a, "Not trusting "+folder+"; /trust changes that", func(ctx context.Context) (app.ReloadReport, error) {
				return a.DenyTrust(ctx)
			})
		}}
		switch {
		case t.ForRun:
			lead = folder + " is trusted for this run by --trust; what you decide here holds from the next:"
		case t.Denied:
			lead = folder + " is not trusted. Trusted, it would turn on:"
		case len(t.Held()) == 0:
			lead = folder + " is trusted, which turns on:"
		case len(t.Agreed) > 0:
			lead = fmt.Sprintf("%s is trusted to %d of these:", folder, len(t.Agreed))
		}
	case len(t.Agreed) > 0:
		lead = folder + " has more to turn on since you trusted it:"
	}
	trust := func(agreed app.Surface) tea.Cmd {
		done := "Trusted " + folder
		if n := len(items) - len(agreed); n > 0 {
			done += fmt.Sprintf(", but for %d you declined", n)
		}
		return decide(a, done, func(ctx context.Context) (app.ReloadReport, error) {
			return a.SetTrust(ctx, items, agreed)
		})
	}
	return panel.NewConsent(key, "Trust this folder?", lead, items, checked, []panel.Choice{
		{Label: "Trust this folder", Pick: trust},
		no,
	}, func() tea.Cmd { return nil })
}

func decide(a *app.App, done string, change func(context.Context) (app.ReloadReport, error)) tea.Cmd {
	return func() tea.Msg {
		r, err := change(context.Background())
		if err != nil {
			return transcript.Fail("Could not change the folder's trust: " + err.Error())
		}
		text := done + connected(r.MCP)
		if p := PendingPlugins(a); p != "" {
			text += " · " + p
		}
		return transcript.Note(text)
	}
}

func trust(a *app.App) Command {
	return Command{Name: "trust", Description: "Decide what of this folder's hooks, MCP servers, plugins and allow rules take effect", Run: func(string) tea.Cmd {
		t := a.Trust()
		switch {
		case t.Root == "":
			return note("The home directory is no project: its settings are yours, in effect as they are.")
		case len(t.Surface) == 0:
			return note(transcript.HomePath(t.Root) + " has nothing that needs trust: no hooks, MCP servers, plugins, allow rules, roots or skill commands.")
		}
		return show(TrustPanel(a, true))
	}}
}
