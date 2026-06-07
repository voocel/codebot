package ui

import (
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/internal/ui/commands"
	"github.com/voocel/codebot/internal/ui/tui"
)

func (a *App) planPhase() plan.Phase {
	if a.PlanManager == nil {
		return plan.PhaseOff
	}
	return a.PlanManager.Snapshot().Phase
}

func (a *App) enterPlanMode() tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	prompt, err := a.PlanManager.Enter()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return a.sendAsPrompt(prompt)
}

func (a *App) cancelPlanMode() tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	if a.planPhase() == plan.PhaseOff {
		return tui.SendCommandResult(tui.CommandStyle.Render("Not in plan mode."))
	}
	if err := a.PlanManager.Cancel(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return tui.SendCommandResult(tui.CommandStyle.Render("Plan mode cancelled. All tools restored."))
}

func (a *App) resetPlanState() {
	if a.PlanManager != nil {
		_ = a.PlanManager.Cancel()
	}
}

func (a *App) resetSessionState() {
	a.resetPlanState()
	if a.GoalManager != nil {
		_, _ = a.GoalManager.Clear()
	}
}

// planOnEvent reacts to enter_plan_mode / exit_plan_mode tool completion.
// Both tools own their own state transitions via plan.Manager; this handler
// only emits scrollback artifacts so the user can see what was approved.
func (a *App) planOnEvent(m *tui.Model, ev agentcore.Event) tea.Cmd {
	if ev.Type == agentcore.EventToolExecEnd && !ev.IsError && ev.Tool == "exit_plan_mode" {
		return a.onExitPlanMode(m, ev.Result)
	}
	return nil
}

func (a *App) onExitPlanMode(m *tui.Model, result json.RawMessage) tea.Cmd {
	var resp struct {
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil
	}
	content := strings.TrimSpace(resp.Plan)
	if content == "" {
		return nil
	}
	return m.Emit(a.renderPlanForReview(m, content))
}

// renderPlanForReview formats the approved plan for scrollback. The user
// already approved it via the permission card; this trail keeps the plan
// visible alongside the model's subsequent execution.
func (a *App) renderPlanForReview(m *tui.Model, content string) string {
	var b strings.Builder
	b.WriteString(tui.ToolIconStyle.Render("● ") + tui.ToolNameStyle.Render("Plan approved"))
	if title := plan.ExtractTitle(content); title != "" && title != "(untitled)" {
		b.WriteString(tui.MutedStyle.Render(" — " + title))
	}
	b.WriteString("\n\n")
	b.WriteString(m.RenderMarkdownBlock(content, 2))
	if path := a.PlanManager.CurrentPlanPath(); path != "" {
		b.WriteString("\n\n")
		b.WriteString(tui.MutedStyle.Render("  Plan saved to: " + path))
	}
	return b.String()
}

func (a *App) showCurrentPlan() tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	content, err := a.PlanManager.CurrentPlan()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	path := a.PlanManager.CurrentPlanPath()
	if strings.TrimSpace(content) == "" {
		if a.planPhase() == plan.PhasePlanning {
			return tui.SendCommandResult(tui.CommandStyle.Render("Already in plan mode. No plan written yet."))
		}
		return tui.SendCommandResult(tui.CommandStyle.Render("No current plan."))
	}
	var sb strings.Builder
	sb.WriteString("Current Plan\n\n")
	if path != "" {
		sb.WriteString("Path: " + path + "\n")
	}
	sb.WriteString("Phase: " + string(a.planPhase()) + "\n\n")
	sb.WriteString(content)
	if path != "" {
		sb.WriteString("\n\nUse /plan open to edit this plan in your editor.")
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (a *App) openCurrentPlan() tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	path := a.PlanManager.CurrentPlanPath()
	if path == "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No active plan file."))
	}
	return commands.OpenEditor(path, "Plan reloaded.", func() { a.Session.Reload() })
}
