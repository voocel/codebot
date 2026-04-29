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

func (a *App) currentPlanTitle() string {
	state := a.PlanManager.Snapshot()
	if strings.TrimSpace(state.Title) != "" {
		return state.Title
	}
	content, err := a.PlanManager.CurrentPlan()
	if err != nil {
		return "(untitled)"
	}
	return plan.ExtractTitle(content)
}

func (a *App) currentAllowedCommands() []plan.AllowedCommand {
	if a.PlanManager == nil {
		return nil
	}
	return a.PlanManager.Snapshot().AllowedCommands
}

func (a *App) allowedCommandLines() []string {
	labels := plan.DescribeAllowedCommands(a.currentAllowedCommands())
	if len(labels) == 0 {
		return nil
	}
	lines := []string{"Allowed command prefixes:"}
	for _, label := range labels {
		lines = append(lines, "- "+label)
	}
	return lines
}

func (a *App) enterPlanMode(task string) tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	prompt, err := a.PlanManager.Enter(task)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return a.sendAsPrompt(prompt)
}

func (a *App) executePlan() tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	if _, _, _, err := a.PlanManager.Approve(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	a.planOtherMode = false
	a.planOtherBuf = ""
	a.planChoice = 0

	return a.sendAsPrompt("The plan has been approved. Execute it now.")
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
	a.resetPlanUI()
	return tui.SendCommandResult(tui.CommandStyle.Render("Plan mode cancelled. All tools restored."))
}

func (a *App) resetPlanState() {
	if a.PlanManager != nil {
		_ = a.PlanManager.Cancel()
	}
	a.resetPlanUI()
}

func (a *App) resetPlanUI() {
	a.planChoice = 0
	a.planOtherMode = false
	a.planOtherBuf = ""
}

const planOptionCount = 3

func (a *App) handlePlanReviewKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if a.planOtherMode {
		return a.handlePlanOtherInput(msg)
	}

	switch msg.String() {
	case "esc":
		return true, a.cancelPlanMode()
	case "ctrl+c":
		return true, a.cancelPlanMode()
	case "ctrl+e":
		// Open the plan in $EDITOR. Mirrors Claude Code's Ctrl+G shortcut on
		// the Exit-Plan-Mode dialog. Reusing /plan open gives us the session
		// reload after the editor exits, so user edits land before approval.
		return true, a.openCurrentPlan()
	case "up", "k":
		if a.planChoice > 0 {
			a.planChoice--
		}
		return true, nil
	case "down", "j", "tab":
		if a.planChoice < planOptionCount-1 {
			a.planChoice++
		}
		return true, nil
	case "enter":
		return a.handlePlanEnter()
	}

	if len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < planOptionCount {
				a.planChoice = idx
				return a.handlePlanEnter()
			}
		}
	}

	return true, nil
}

func (a *App) handlePlanEnter() (bool, tea.Cmd) {
	switch a.planChoice {
	case 0:
		return true, a.executePlan()
	case 1:
		return true, a.cancelPlanMode()
	default:
		a.planOtherMode = true
		a.planOtherBuf = ""
		return true, nil
	}
}

func (a *App) handlePlanOtherInput(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.planOtherMode = false
		a.planOtherBuf = ""
		return true, nil
	case "enter":
		text := strings.TrimSpace(a.planOtherBuf)
		if text == "" {
			return true, nil
		}
		a.planOtherMode = false
		a.planOtherBuf = ""
		return true, a.editPlanWithFeedback(text)
	case "backspace":
		if len(a.planOtherBuf) > 0 {
			runes := []rune(a.planOtherBuf)
			a.planOtherBuf = string(runes[:len(runes)-1])
		}
		return true, nil
	default:
		if len(msg.Runes) > 0 {
			a.planOtherBuf += string(msg.Runes)
		}
		return true, nil
	}
}

func (a *App) editPlanWithFeedback(feedback string) tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	prompt, err := a.PlanManager.Revise(feedback)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return a.sendAsPrompt(prompt)
}

func (a *App) planOnEvent(m *tui.Model, ev agentcore.Event) tea.Cmd {
	if ev.Type == agentcore.EventToolExecEnd && !ev.IsError {
		switch ev.Tool {
		case "enter_plan_mode":
			return a.onEnterPlanMode(m, ev.Result)
		case "exit_plan_mode":
			return a.onExitPlanMode(m, ev.Result)
		}
	}
	// Plan review state is rendered live by RenderPlanBar (driven by
	// planStatus). The plan content is on disk (model wrote it via
	// write/edit during planning); we deliberately don't emit a scrollback
	// summary on EventAgentEnd.
	return nil
}

func (a *App) onEnterPlanMode(m *tui.Model, result json.RawMessage) tea.Cmd {
	if a.planPhase() != plan.PhaseOff {
		return nil
	}
	if a.PlanManager == nil {
		return nil
	}
	var resp struct {
		Task string `json:"task"`
	}
	_ = json.Unmarshal(result, &resp)
	if _, err := a.PlanManager.Enter(strings.TrimSpace(resp.Task)); err != nil {
		return m.Emit(tui.ErrorStyle.Render("Plan mode error: " + err.Error()))
	}
	return nil
}

func (a *App) onExitPlanMode(m *tui.Model, result json.RawMessage) tea.Cmd {
	if a.planPhase() != plan.PhasePlanning || a.PlanManager == nil {
		return nil
	}

	var resp struct {
		Title           string                   `json:"title"`
		AllowedCommands []plan.RawAllowedCommand `json:"allowed_commands"`
	}
	_ = json.Unmarshal(result, &resp)

	commands := plan.ParseAllowedCommands(resp.AllowedCommands)
	// Submit reads the plan content from disk — the model wrote it
	// incrementally via write/edit during planning.
	if err := a.PlanManager.Submit(strings.TrimSpace(resp.Title), commands); err != nil {
		return m.Emit(tui.ErrorStyle.Render("Plan submit error: " + err.Error()))
	}

	a.planChoice = 0
	a.planOtherMode = false
	a.planOtherBuf = ""

	// Emit the full plan to scrollback so the user can read it before
	// approving. The model wrote it to the plan file via write/edit; the
	// per-call Write preview only shows the first 12 lines, so without this
	// the user would see the title + path in the review card but not the
	// content itself. Mirrors Claude Code's ExitPlanMode tool-result render.
	var emitPlan tea.Cmd
	if content, err := a.PlanManager.CurrentPlan(); err == nil && strings.TrimSpace(content) != "" {
		emitPlan = m.Emit(a.renderPlanForReview(m, content))
	}

	// exit_plan_mode succeeded — stop the agent so the user can review the
	// plan. Without this the loop keeps running and the model would chatter
	// past the review card.
	flush := m.FlushStreamingAssistant()
	a.Session.AbortSilent()
	return tea.Batch(flush, emitPlan)
}

func (a *App) renderPlanForReview(m *tui.Model, content string) string {
	var b strings.Builder
	b.WriteString(tui.ToolIconStyle.Render("● ") + tui.ToolNameStyle.Render("Plan"))
	if title := strings.TrimSpace(a.currentPlanTitle()); title != "" && title != "(untitled)" {
		b.WriteString(tui.MutedStyle.Render(" — " + title))
	}
	b.WriteString("\n\n")
	// Indent the body 2 spaces so the plan aligns under the "● Plan" header,
	// matching the visual layout of every other tool result block in events.go.
	b.WriteString(m.RenderMarkdownBlock(strings.TrimSpace(content), 2))
	if path := a.PlanManager.CurrentPlanPath(); path != "" {
		b.WriteString("\n\n")
		b.WriteString(tui.MutedStyle.Render("  Plan saved to: " + path + " · /plan open to edit"))
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
	sb.WriteString("Phase: " + string(a.planPhase()) + "\n")
	for _, line := range a.allowedCommandLines() {
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n")
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

func (a *App) planStatus(m *tui.Model) *tui.PlanBarInfo {
	if a.planPhase() != plan.PhaseReview || m.Running {
		return nil
	}
	var filePath string
	if a.PlanManager != nil {
		filePath = a.PlanManager.CurrentPlanPath()
	}
	return &tui.PlanBarInfo{
		Title:        a.currentPlanTitle(),
		PlanFilePath: filePath,
		Details:      a.planReviewDetails(),
		Choices:      []string{"Execute plan", "Exit plan mode"},
		Active:       a.planChoice,
		OtherMode:    a.planOtherMode,
		OtherBuf:     a.planOtherBuf,
	}
}

func (a *App) planReviewDetails() []string {
	return a.allowedCommandLines()
}
