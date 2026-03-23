package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/ui/tui"
)

// BtwCommand implements /btw — ephemeral side-chain Q&A that reads the full
// conversation context but never mutates history or invokes tools.
type BtwCommand struct {
	app *App

	active   bool
	question string
	answer   string
	loading  bool
	err      error
}

// NewBtwCommand creates a /btw command bound to the given App.
func NewBtwCommand(app *App) *BtwCommand {
	return &BtwCommand{app: app}
}

func (c *BtwCommand) Spec() CommandSpec {
	return CommandSpec{
		Name:        "btw",
		Usage:       "/btw <question>",
		Description: "Ask a quick side question (ephemeral, no history)",
		Category:    "info",
		Kind:        CommandKindBuiltin,
		Placeholder: "<question>",
	}
}

func (c *BtwCommand) Run(_ *CommandContext, inv CommandInvocation) tea.Cmd {
	question := strings.TrimSpace(inv.RawArgs)
	if question == "" {
		return tui.SendCommandResult(tui.CommandStyle.Render("Usage: /btw <your question>"))
	}

	c.question = question
	c.answer = ""
	c.loading = true
	c.err = nil
	c.active = true
	c.app.registry.SetOverlay(c)

	return func() tea.Msg {
		answer, err := c.app.Session.SideQuestion(context.Background(), question)
		return tui.BtwResultMsg{Answer: answer, Err: err}
	}
}

func (c *BtwCommand) Active() bool { return c.active }

func (c *BtwCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case " ", "enter", "esc", "ctrl+c":
		c.app.registry.ClearOverlay()
		return true, nil
	}
	return true, nil // consume all keys while overlay is active
}

func (c *BtwCommand) View(width int) string {
	maxWidth := max(width-4, 20)

	var sb strings.Builder

	// Question header
	qStyle := lipgloss.NewStyle().Foreground(tui.ColorUser).Bold(true)
	sb.WriteString(qStyle.Render("btw: ") + c.question)

	// Blank line before answer body
	sb.WriteString("\n\n")

	// Answer body or loading/error state
	if c.err != nil {
		sb.WriteString(tui.ErrorStyle.Render(fmt.Sprintf("Error: %v", c.err)))
	} else if c.loading {
		sb.WriteString(tui.MutedStyle.Render("Thinking..."))
	} else {
		sb.WriteString(c.answer)
	}

	// Blank line before hint
	sb.WriteString("\n\n")

	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "248", Dark: "235"}).
		Italic(true)
	sb.WriteString(hintStyle.Render("Press Space, Enter, or Esc to dismiss"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tui.ColorAccent).
		Padding(0, 1).
		MaxWidth(maxWidth).
		Render(sb.String())

	return box
}

func (c *BtwCommand) Dismiss() {
	c.active = false
	c.loading = false
}

// SetResult updates the overlay with the side question response.
func (c *BtwCommand) SetResult(msg tui.BtwResultMsg) {
	c.loading = false
	if msg.Err != nil {
		c.err = msg.Err
	} else {
		c.answer = msg.Answer
	}
}
