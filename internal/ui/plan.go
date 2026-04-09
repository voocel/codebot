package ui

import (
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/plan"
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

func (a *App) cmdPlan(args []string) tea.Cmd {
	if len(args) == 0 {
		switch a.planPhase() {
		case plan.PhaseOff:
			return a.enterPlanMode("")
		case plan.PhasePlanning:
			return a.showCurrentPlan()
		case plan.PhaseReview:
			return a.showCurrentPlan()
		}
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "cancel":
		return a.cancelPlanMode()
	case "open":
		return a.openCurrentPlan()
	default:
		if a.planPhase() != plan.PhaseOff {
			return tui.SendCommandResult(tui.ErrorStyle.Render(
				"Already in plan mode. Use /plan open to inspect the plan, or /plan cancel to exit first."))
		}
		return a.enterPlanMode(strings.Join(args, " "))
	}
}

func (a *App) enterPlanMode(task string) tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	prompt, err := a.PlanManager.Enter(task)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	a.planTitle = ""
	return a.sendAsPrompt(prompt)
}

func (a *App) executePlan() tea.Cmd {
	if a.PlanManager == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan manager is not available."))
	}
	title, _, _, err := a.PlanManager.Approve()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	a.planTitle = title
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
	a.planTitle = ""
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
	a.planTitle = ""
	return a.sendAsPrompt(prompt)
}

func (a *App) planOnEvent(_ *tui.Model, ev agentcore.Event) tea.Cmd {
	switch ev.Type {
	case agentcore.EventToolExecEnd:
		if ev.IsError {
			return nil
		}
		switch ev.Tool {
		case "enter_plan_mode":
			return a.onEnterPlanMode(ev.Result)
		case "exit_plan_mode":
			return a.onExitPlanMode(ev.Result)
		}
	case agentcore.EventAgentEnd:
		if a.planPhase() == plan.PhaseReview {
			title := tui.ChoiceActiveStyle.Render("Plan: " + a.currentPlanTitle())
			lines := []string{title}
			for _, detail := range a.allowedCommandLines() {
				lines = append(lines, tui.MutedStyle.Render(detail))
			}
			lines = append(lines, tui.MutedStyle.Render("Select an action below."))
			box := tui.PlanBoxStyle.Render(strings.Join(lines, "\n"))
			return tea.Println("\n" + box)
		}
	}
	return nil
}

func (a *App) onEnterPlanMode(result json.RawMessage) tea.Cmd {
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
		return tea.Println(tui.ErrorStyle.Render("Plan mode error: " + err.Error()))
	}
	a.planTitle = ""
	return nil
}

func (a *App) onExitPlanMode(result json.RawMessage) tea.Cmd {
	if a.planPhase() != plan.PhasePlanning || a.PlanManager == nil {
		return nil
	}

	var resp struct {
		Title           string                   `json:"title"`
		Content         string                   `json:"content"`
		AllowedCommands []plan.RawAllowedCommand `json:"allowed_commands"`
	}
	_ = json.Unmarshal(result, &resp)

	content := resp.Content
	if strings.TrimSpace(content) == "" {
		content = a.Session.LastAssistantText()
	}
	title := strings.TrimSpace(resp.Title)
	if title == "" {
		title = plan.ExtractTitle(content)
	}
	commands := plan.ParseAllowedCommands(resp.AllowedCommands)
	if err := a.PlanManager.Submit(title, content, commands); err != nil {
		return tea.Println(tui.ErrorStyle.Render("Plan submit error: " + err.Error()))
	}

	a.planTitle = title
	a.planChoice = 0
	a.planOtherMode = false
	a.planOtherBuf = ""
	a.Session.AbortSilent()
	return nil
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
	return a.openEditor(path, "Plan reloaded.")
}

func (a *App) planStatus(m *tui.Model) *tui.PlanBarInfo {
	if a.planPhase() != plan.PhaseReview || m.Running {
		return nil
	}
	return &tui.PlanBarInfo{
		Prompt:    "Would you like to proceed?",
		Details:   a.planReviewDetails(),
		Choices:   []string{"Execute plan", "Cancel"},
		Active:    a.planChoice,
		OtherMode: a.planOtherMode,
		OtherBuf:  a.planOtherBuf,
	}
}

func (a *App) planReviewDetails() []string {
	return a.allowedCommandLines()
}
