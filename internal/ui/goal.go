package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/bootstrap"
	"github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/ui/tui"
)

func (a *App) createGoal(objective string, tokenBudget int) tea.Cmd {
	if a.GoalManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Goal manager is not available."))
	}
	state, err := a.GoalManager.CreateWithBudget(objective, tokenBudget)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	a.wireGoalTools()
	return a.sendAsPrompt(goal.StartPrompt(state))
}

func (a *App) showGoalStatus() tea.Cmd {
	if a.GoalManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Goal manager is not available."))
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(formatGoalStatus(a.GoalManager.Snapshot())))
}

func (a *App) pauseGoal() tea.Cmd {
	if a.GoalManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Goal manager is not available."))
	}
	if _, err := a.GoalManager.Pause(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return nil
}

func (a *App) resumeGoal(tokenBudget int) tea.Cmd {
	if a.GoalManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Goal manager is not available."))
	}
	state, err := a.GoalManager.ResumeWithBudget(tokenBudget)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return a.sendAsPrompt(goal.StartPrompt(state))
}

func (a *App) clearGoal() tea.Cmd {
	if a.GoalManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Goal manager is not available."))
	}
	if _, err := a.GoalManager.Clear(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return nil
}

// wireGoalTools re-attaches goal tool callbacks after the session toolset is
// rebuilt (plugin reload). Boot performs the initial wiring.
func (a *App) wireGoalTools() {
	bootstrap.WireGoalTools(a.Session, a.GoalManager)
}

func (a *App) restoreSessionGoalState() error {
	if a.Session == nil || a.GoalManager == nil {
		return nil
	}
	snapshot, err := a.Session.CurrentSnapshot()
	if err != nil {
		return err
	}
	if err := a.GoalManager.Restore(goal.StateFromEntry(snapshot.Goal)); err != nil {
		return err
	}
	a.wireGoalTools()
	return nil
}

func formatGoalStatus(state goal.State) string {
	state = state.Normalize()
	if state.Status == goal.StatusOff {
		return "No active goal."
	}

	var sb strings.Builder
	sb.WriteString("Goal\n\n")
	sb.WriteString("Status: " + string(state.Status) + "\n")
	if state.ID != "" {
		sb.WriteString("ID: " + state.ID + "\n")
	}
	sb.WriteString("Objective: " + state.Objective)
	if state.Reason != "" {
		sb.WriteString("\nReason: " + state.Reason)
	}
	if state.TokenBudget > 0 {
		remaining := state.TokenBudget - state.TokensUsed
		if remaining < 0 {
			remaining = 0
		}
		sb.WriteString(fmt.Sprintf("\nTokens: %d/%d used, %d remaining", state.TokensUsed, state.TokenBudget, remaining))
	} else if state.TokensUsed > 0 {
		sb.WriteString(fmt.Sprintf("\nTokens used: %d", state.TokensUsed))
	}
	if state.BlockedReason != "" {
		sb.WriteString(fmt.Sprintf("\nBlocked audit: %d/3", state.BlockedCount))
		sb.WriteString("\nBlocked reason: " + state.BlockedReason)
	}
	if !state.UpdatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("\nUpdated: %s", state.UpdatedAt.Format("2006-01-02 15:04:05")))
	}
	return sb.String()
}
