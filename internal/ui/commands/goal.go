package commands

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/ui/tui"
)

type GoalCommand struct {
	Create func(objective string, tokenBudget int) tea.Cmd
	Status func() tea.Cmd
	Pause  func() tea.Cmd
	Resume func(tokenBudget int) tea.Cmd
	Clear  func() tea.Cmd
}

func (c *GoalCommand) Spec() Spec {
	return Spec{
		Name:        "goal",
		Usage:       "/goal [--tokens N] <objective>|status|pause|resume [--tokens N]|clear",
		Description: "Set or manage an explicit session goal",
		Category:    "goal",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *GoalCommand) Run(inv Invocation) tea.Cmd {
	arg := strings.TrimSpace(inv.RawArgs)
	if arg == "" || strings.EqualFold(arg, "status") {
		return c.Status()
	}

	fields := strings.Fields(arg)
	if len(fields) > 0 && strings.EqualFold(fields[0], "resume") {
		if c.Resume == nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Goal manager is not available."))
		}
		tokenBudget, err := parseGoalResumeArgs(strings.Join(fields[1:], " "))
		if err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
		}
		return c.Resume(tokenBudget)
	}

	switch strings.ToLower(arg) {
	case "pause":
		return c.Pause()
	case "clear":
		return c.Clear()
	}

	if c.Create == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Goal manager is not available."))
	}
	objective, tokenBudget, err := parseGoalCreateArgs(arg)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return c.Create(objective, tokenBudget)
}

func parseGoalCreateArgs(arg string) (string, int, error) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", 0, fmt.Errorf("goal objective is required")
	}
	if fields[0] != "--tokens" && !strings.HasPrefix(fields[0], "--tokens=") {
		return arg, 0, nil
	}

	var rawBudget string
	var rest []string
	if fields[0] == "--tokens" {
		if len(fields) < 3 {
			return "", 0, fmt.Errorf("usage: /goal --tokens N <objective>")
		}
		rawBudget = fields[1]
		rest = fields[2:]
	} else {
		rawBudget = strings.TrimPrefix(fields[0], "--tokens=")
		rest = fields[1:]
		if rawBudget == "" || len(rest) == 0 {
			return "", 0, fmt.Errorf("usage: /goal --tokens=N <objective>")
		}
	}

	tokenBudget, err := strconv.Atoi(rawBudget)
	if err != nil || tokenBudget <= 0 {
		return "", 0, fmt.Errorf("goal token budget must be a positive integer")
	}
	return strings.Join(rest, " "), tokenBudget, nil
}

func parseGoalResumeArgs(arg string) (int, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return 0, nil
	}
	fields := strings.Fields(arg)

	var rawBudget string
	switch {
	case len(fields) == 2 && fields[0] == "--tokens":
		rawBudget = fields[1]
	case len(fields) == 1 && strings.HasPrefix(fields[0], "--tokens="):
		rawBudget = strings.TrimPrefix(fields[0], "--tokens=")
	default:
		return 0, fmt.Errorf("usage: /goal resume [--tokens N]")
	}

	tokenBudget, err := strconv.Atoi(rawBudget)
	if err != nil || tokenBudget <= 0 {
		return 0, fmt.Errorf("goal token budget must be a positive integer")
	}
	return tokenBudget, nil
}
