package commands

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// trustKey identifies the panel deciding on the folder's trust.
var trustKey = new(int)

// IsTrust reports whether p decides on the folder's trust.
func IsTrust(p panel.Panel) bool {
	r, ok := p.(panel.Request)
	return ok && r.Key() == trustKey
}

// TrustPanel asks the user to decide on the folder's trust: on what it
// asks about, or with all, on its whole surface. Dismissed, it keeps things
// as they stand for the session.
func TrustPanel(a *app.App, all bool) panel.Panel {
	t := a.Trust()
	folder := transcript.HomePath(t.Root)
	items, lead := t.Ask, folder+" would turn on:"
	switch {
	case all:
		items = t.Surface
		if t.Trusted {
			lead = folder + " is trusted, which turns on:"
		}
	case len(t.Ask) < len(t.Surface):
		lead = folder + " has more to turn on since you trusted it:"
	}
	decide := func(trusted, remember bool) func() tea.Cmd {
		return func() tea.Cmd { return setTrust(a, t, trusted, remember) }
	}
	return panel.NewConsent(trustKey, "Trust this folder?", lead, items, []panel.Choice{
		{Label: "Trust this folder", Pick: decide(true, true)},
		{Label: "Trust for this session", Pick: decide(true, false)},
		{Label: "Don't trust · keep working without them", Pick: decide(false, true)},
	}, decide(t.Trusted, false))
}

func setTrust(a *app.App, t app.Trust, trusted, remember bool) tea.Cmd {
	return func() tea.Msg {
		r, err := a.SetTrust(context.Background(), t.Surface, trusted, remember)
		if err != nil {
			return transcript.Fail("Could not change the folder's trust: " + err.Error())
		}
		folder := transcript.HomePath(t.Root)
		switch {
		case trusted == t.Trusted && !remember:
			return nil
		case !trusted:
			return transcript.Note("Not trusting " + folder + "; /trust changes that")
		}
		text := "Trusted " + folder
		if !remember {
			text += " for this session"
		}
		return transcript.Note(text + connected(r))
	}
}

func trust(a *app.App) Command {
	return Command{Name: "trust", Description: "Decide whether this folder's hooks, MCP servers, plugins and allow rules take effect", Run: func(string) tea.Cmd {
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
