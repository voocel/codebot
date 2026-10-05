package commands

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ContextCommand drives /context — a modal overlay reporting current context
// window usage.
type ContextCommand struct {
	app     *app.App
	overlay OverlayController

	state *contextState
}

type contextState struct {
	used, window        int
	messages, summaries int
}

// Context constructs the /context command.
func Context(a *app.App, overlay OverlayController) *ContextCommand {
	return &ContextCommand{app: a, overlay: overlay}
}

func (c *ContextCommand) Spec() Spec {
	return Spec{
		Name:        "context",
		Usage:       "/context",
		Description: "Show current context usage",
		Kind:        KindBuiltin,
	}
}

func (c *ContextCommand) Run(_ Invocation) tea.Cmd {
	conv := c.app.Current()
	st := conv.Status()
	state := &contextState{used: st.Context, window: st.Window}
	for _, m := range conv.History() {
		state.messages++
		if m.Kind == agentcore.KindSummary {
			state.summaries++
		}
	}
	c.state = state
	c.overlay.SetOverlay(c)
	return nil
}

func (c *ContextCommand) Active() bool { return c.state != nil }
func (c *ContextCommand) Dismiss()     { c.state = nil }

func (c *ContextCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		c.overlay.ClearOverlay()
	}
	return true, nil
}

func (c *ContextCommand) View(width, height int) string {
	if c.state == nil {
		return ""
	}
	frame := tui.InfoOverlayFrame{
		Title:  "Context",
		Tabs:   []tui.InfoOverlayTab{{Name: "usage", Body: c.renderUsage}},
		Hint:   "Esc close",
		Width:  width,
		Height: height,
	}
	return frame.Render()
}

func (c *ContextCommand) renderUsage(width int) string {
	s := c.state
	p := tui.NewInfoPanel(width)
	p.Row("Used", formatContext(s.used, s.window))
	p.Row("Window", tui.FormatTokens(s.window))
	p.Row("Messages", fmt.Sprintf("%d", s.messages))
	p.Row("Summaries", fmt.Sprintf("%d", s.summaries))
	return p.Render()
}
