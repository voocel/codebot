package commands

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/ui/tui"
)

// BtwCommand drives /btw — an ephemeral side-chain Q&A that consults the
// current conversation context but never mutates history or invokes tools.
// It is exported so the host can route the async tui.BtwResultMsg back to
// the active overlay via SetResult.
type BtwCommand struct {
	session  *agent.Session
	registry Registry

	active   bool
	question string
	answer   string
	loading  bool
	err      error
}

// Btw constructs the /btw command bound to the given session and registry.
func Btw(session *agent.Session, registry Registry) *BtwCommand {
	return &BtwCommand{session: session, registry: registry}
}

func (c *BtwCommand) Spec() Spec {
	return Spec{
		Name:        "btw",
		Usage:       "/btw <question>",
		Description: "Ask a quick side question (ephemeral, no history)",
		Category:    "info",
		Kind:        KindBuiltin,
	}
}

func (c *BtwCommand) Run(inv Invocation) tea.Cmd {
	question := strings.TrimSpace(inv.RawArgs)
	if question == "" {
		return tui.SendCommandResult(tui.CommandStyle.Render("Usage: /btw <your question>"))
	}

	c.question = question
	c.answer = ""
	c.loading = true
	c.err = nil
	c.active = true
	c.registry.SetOverlay(c)

	return func() tea.Msg {
		answer, err := c.session.SideQuestion(context.Background(), question)
		return tui.BtwResultMsg{Answer: answer, Err: err}
	}
}

func (c *BtwCommand) Active() bool  { return c.active }
func (c *BtwCommand) IsModal() bool { return true }

func (c *BtwCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case " ", "enter", "esc", "ctrl+c":
		c.registry.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *BtwCommand) View(width, _ int) string {
	maxWidth := max(width-4, 20)

	var sb strings.Builder
	qStyle := lipgloss.NewStyle().Foreground(tui.RoleUser).Bold(true)
	sb.WriteString(qStyle.Render("btw: ") + c.question)
	sb.WriteString("\n\n")

	if c.err != nil {
		sb.WriteString(tui.ErrorStyle.Render(fmt.Sprintf("Error: %v", c.err)))
	} else if c.loading {
		sb.WriteString(tui.MutedStyle.Render("Thinking..."))
	} else {
		sb.WriteString(c.answer)
	}

	sb.WriteString("\n\n")
	hintStyle := lipgloss.NewStyle().Foreground(tui.Subtle).Italic(true)
	sb.WriteString(hintStyle.Render("Press Space, Enter, or Esc to dismiss"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tui.Accent).
		Padding(0, 1).
		MaxWidth(maxWidth).
		Render(sb.String())
}

func (c *BtwCommand) Dismiss() {
	c.active = false
	c.loading = false
}

// SetResult updates the overlay with the side question response. The host
// invokes this from its OnBtwResult hook.
func (c *BtwCommand) SetResult(msg tui.BtwResultMsg) {
	c.loading = false
	if msg.Err != nil {
		c.err = msg.Err
	} else {
		c.answer = msg.Answer
	}
}
