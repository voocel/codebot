package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/tools"
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

func (a *App) wireGoalTools() {
	if a.Session == nil || a.GoalManager == nil {
		return
	}
	a.Session.SetGoalUsageLimitHandler(func(reason string) (goal.State, error) {
		state := a.GoalManager.Snapshot().Normalize()
		if state.Status != goal.StatusActive && state.Status != goal.StatusBudgetLimited {
			return state, nil
		}
		return a.GoalManager.UsageLimit(reason)
	})
	for _, tool := range a.Session.ToolsByName("create_goal", "get_goal", "update_goal") {
		switch t := tool.(type) {
		case *tools.GoalCreateTool:
			t.SetCreator(a.GoalManager.CreateWithBudget)
		case *tools.GoalGetTool:
			t.SetSnapshotter(a.GoalManager.Snapshot)
		case *tools.GoalUpdateTool:
			t.SetHandlers(a.GoalManager.Complete, a.GoalManager.Block)
		}
	}
}

func (a *App) restoreSessionGoalState() error {
	if a.Session == nil || a.GoalManager == nil {
		return nil
	}
	snapshot, err := a.Session.CurrentSnapshot()
	if err != nil {
		return err
	}
	if err := a.GoalManager.Restore(restoreGoalState(snapshot.Goal)); err != nil {
		return err
	}
	a.wireGoalTools()
	return nil
}

func restoreGoalState(entry storage.GoalStateEntry) goal.State {
	return goal.State{
		ID:                       entry.ID,
		Objective:                entry.Objective,
		Status:                   goal.Status(entry.Status),
		CreatedAt:                entry.CreatedAt,
		UpdatedAt:                entry.UpdatedAt,
		CompletedAt:              entry.CompletedAt,
		BlockedAt:                entry.BlockedAt,
		BudgetLimitedAt:          entry.BudgetLimitedAt,
		UsageLimitedAt:           entry.UsageLimitedAt,
		Reason:                   entry.Reason,
		BlockedReason:            entry.BlockedReason,
		BlockedCount:             entry.BlockedCount,
		BlockedAttemptTokenTotal: entry.BlockedAttemptTokenTotal,
		BudgetLimitReported:      entry.BudgetLimitReported,
		TokenBudget:              entry.TokenBudget,
		TokensUsed:               entry.TokensUsed,
		TokenTotalAtLastAccount:  entry.TokenTotalAtLastAccount,
	}.Normalize()
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
