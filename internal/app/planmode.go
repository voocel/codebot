package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/tui"
)

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type planState int

const (
	planOff       planState = iota
	planPlanning            // read-only exploration + exit_plan_mode tool
	planPending             // plan submitted, awaiting user approval
	planExecuting           // full tools + mark_step tool
)

type planStep struct {
	Number    int
	Text      string
	Completed bool
}

// ---------------------------------------------------------------------------
// System prompts
// ---------------------------------------------------------------------------

const planModePrompt = `[PLAN MODE - Read-Only]
You are in plan mode. Explore and analyze the codebase, then create a detailed implementation plan.

Available tools: read, find, grep, ls, bash (read-only commands only), exit_plan_mode
Disabled tools: write, edit

When your plan is ready, call the exit_plan_mode tool with structured steps.
Do NOT write the plan as plain text — you MUST use the exit_plan_mode tool.
Do NOT modify any files.`

func buildExecutionPrompt(steps []planStep) string {
	var sb strings.Builder
	sb.WriteString("[EXECUTING PLAN]\n")
	sb.WriteString("Execute the following plan. After completing each step, call the mark_step tool.\n\n")
	sb.WriteString("Steps:\n")
	for _, s := range steps {
		if !s.Completed {
			fmt.Fprintf(&sb, "%d. %s\n", s.Number, s.Text)
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Commands — /plan injects prompts, does NOT directly switch state
// ---------------------------------------------------------------------------

func (a *App) cmdPlan(args []string) tea.Cmd {
	if len(args) == 0 {
		switch a.planState {
		case planOff:
			return a.sendAsPrompt("Use the enter_plan_mode tool to begin planning.")
		case planPlanning:
			return tui.SendCommandResult(tui.CommandStyle.Render(
				"Already in plan mode (read-only). Use /plan cancel to abort."))
		case planPending:
			return tui.SendCommandResult(tui.CommandStyle.Render(
				"Plan awaiting approval. Use ↑↓ to select, Enter to confirm."))
		case planExecuting:
			return tui.SendCommandResult(tui.CommandStyle.Render(
				fmt.Sprintf("Executing plan: %d/%d completed. Use /plan cancel to abort.",
					a.completedCount(), len(a.planSteps))))
		}
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "execute":
		return a.executePlan()
	case "cancel":
		return a.cancelPlanMode()
	case "list":
		return a.cmdPlanList()
	case "show":
		id := ""
		if len(args) > 1 {
			id = args[1]
		}
		return a.cmdPlanShow(id)
	default:
		// /plan <task> — inject prompt with task description.
		if a.planState != planOff {
			return tui.SendCommandResult(tui.ErrorStyle.Render(
				"Already in plan mode. Use /plan execute or /plan cancel."))
		}
		msg := strings.Join(args, " ")
		return a.sendAsPrompt(fmt.Sprintf(
			"Use the enter_plan_mode tool to begin planning.\n\nTask: %s", msg))
	}
}

func (a *App) cmdTodos() tea.Cmd {
	if a.planState == planOff || len(a.planSteps) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"No plan steps. Enter plan mode with /plan first."))
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(a.formatSteps()))
}

// ---------------------------------------------------------------------------
// State transitions
// ---------------------------------------------------------------------------

func (a *App) executePlan() tea.Cmd {
	if a.planState != planPlanning && a.planState != planPending {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"Not in planning state. Use /plan to enter plan mode first."))
	}
	if len(a.planSteps) == 0 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"No plan steps submitted. Let the agent call exit_plan_mode first."))
	}

	a.Session.RestoreAllTools(newMarkStepTool())
	a.Session.SetSystemSuffix(buildExecutionPrompt(a.planSteps))
	a.planState = planExecuting

	// Persist status change.
	if a.PlanStore != nil && a.planID != "" {
		_ = a.PlanStore.UpdateStatus(a.planID, plan.StatusExecuting)
	}

	remaining := a.remainingSteps()
	if len(remaining) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("All steps already completed."))
	}
	msg := fmt.Sprintf("Execute the plan. Start with step %d. After completing each step, call the mark_step tool.",
		remaining[0].Number)

	return func() tea.Msg {
		_ = a.Session.Prompt(msg)
		return tui.CommandResultMsg{
			Text: tui.CommandStyle.Render(fmt.Sprintf(
				"Executing plan (%d steps). Progress: /todos", len(a.planSteps))),
		}
	}
}

func (a *App) cancelPlanMode() tea.Cmd {
	if a.planState == planOff {
		return tui.SendCommandResult(tui.CommandStyle.Render("Not in plan mode."))
	}
	// Persist abandoned status before resetting.
	if a.PlanStore != nil && a.planID != "" {
		_ = a.PlanStore.UpdateStatus(a.planID, plan.StatusAbandoned)
	}
	a.resetPlanState()
	return tui.SendCommandResult(tui.CommandStyle.Render("Plan mode cancelled. All tools restored."))
}

func (a *App) resetPlanState() {
	a.Session.RestoreAllTools(newEnterPlanModeTool())
	a.Session.SetSystemSuffix("")
	a.planState = planOff
	a.planSteps = nil
	a.planID = ""
}

// ---------------------------------------------------------------------------
// Event handling — driven by tool-use events
// ---------------------------------------------------------------------------

func (a *App) planOnEvent(_ *tui.Model, ev agentcore.Event) tea.Cmd {
	if ev.Type != agentcore.EventToolExecEnd || ev.IsError {
		return nil
	}

	switch ev.Tool {
	case "enter_plan_mode":
		return a.onEnterPlanMode()
	case "exit_plan_mode":
		return a.onExitPlanMode(ev.Result)
	case "mark_step":
		return a.onMarkStep(ev.Result)
	}
	return nil
}

func (a *App) onEnterPlanMode() tea.Cmd {
	if a.planState != planOff {
		return nil
	}

	// Switch to read-only tools + exit_plan_mode.
	readOnly := a.Session.ToolsByName("read", "find", "grep", "ls", "bash")
	a.Session.SetTools(append(readOnly, newExitPlanModeTool())...)
	a.Session.SetSystemSuffix(planModePrompt)
	a.planState = planPlanning
	a.planSteps = nil

	// Persist draft plan.
	now := time.Now().UnixMilli()
	id := plan.GenerateID()
	a.planID = id
	if a.PlanStore != nil {
		_ = a.PlanStore.Save(&plan.SavedPlan{
			Metadata: plan.Metadata{
				ID:               id,
				Status:           plan.StatusDraft,
				WorkingDirectory: a.Cwd,
				CreatedAt:        now,
				UpdatedAt:        now,
				Version:          1,
			},
		})
	}

	return nil
}

func (a *App) onExitPlanMode(result json.RawMessage) tea.Cmd {
	if a.planState != planPlanning {
		return nil
	}

	var data struct {
		Steps []struct {
			Description string `json:"description"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(result, &data); err != nil || len(data.Steps) == 0 {
		return nil
	}

	a.planSteps = make([]planStep, len(data.Steps))
	for i, s := range data.Steps {
		a.planSteps[i] = planStep{Number: i + 1, Text: s.Description}
	}

	// Persist steps and pending status.
	if a.PlanStore != nil && a.planID != "" {
		if p, _ := a.PlanStore.Load(a.planID); p != nil {
			p.Steps = toPlanSteps(a.planSteps)
			p.Metadata.Status = plan.StatusPending
			p.Metadata.Title = planTitle(a.planSteps)
			_ = a.PlanStore.Save(p)
		}
	}

	// Switch to pending state and stop the agent so the user can review.
	a.planState = planPending
	a.planChoice = 0
	a.Session.Abort()

	return tea.Println("\n" + tui.CommandStyle.Render(a.formatSteps()))
}

func (a *App) onMarkStep(result json.RawMessage) tea.Cmd {
	if a.planState != planExecuting {
		return nil
	}

	var data struct {
		Step int `json:"step"`
	}
	if err := json.Unmarshal(result, &data); err != nil || data.Step <= 0 {
		return nil
	}

	for i := range a.planSteps {
		if a.planSteps[i].Number == data.Step {
			a.planSteps[i].Completed = true
			break
		}
	}

	// Persist step completion.
	if a.PlanStore != nil && a.planID != "" {
		_ = a.PlanStore.UpdateStep(a.planID, data.Step, "completed")
	}

	if a.allStepsCompleted() {
		if a.PlanStore != nil && a.planID != "" {
			_ = a.PlanStore.UpdateStatus(a.planID, plan.StatusCompleted)
		}
		a.resetPlanState()
		return tea.Println("\n" + tui.CommandStyle.Render("Plan completed. All steps done. Restored normal mode."))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Plan list / show commands
// ---------------------------------------------------------------------------

func (a *App) cmdPlanList() tea.Cmd {
	if a.PlanStore == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan store not configured."))
	}
	plans, err := a.PlanStore.List(a.Cwd)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to list plans: " + err.Error()))
	}
	if len(plans) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No plans found for this project."))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plans for %s:\n", a.Cwd)
	for i, p := range plans {
		title := p.Metadata.Title
		if title == "" {
			title = "(untitled)"
		}
		if len([]rune(title)) > 40 {
			title = string([]rune(title)[:40]) + "..."
		}
		completed := p.CompletedCount()
		total := len(p.Steps)
		updated := time.UnixMilli(p.Metadata.UpdatedAt).Format("01-02 15:04")
		fmt.Fprintf(&sb, "  %d. [%s] %s  %q  %d/%d steps  %s\n",
			i+1, p.Metadata.Status, p.Metadata.ID, title, completed, total, updated)
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (a *App) cmdPlanShow(id string) tea.Cmd {
	if a.PlanStore == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan store not configured."))
	}
	// Default to current active plan.
	if id == "" {
		id = a.planID
	}
	if id == "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No plan ID specified. Usage: /plan show <id>"))
	}

	p, err := a.PlanStore.Load(id)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to load plan: " + err.Error()))
	}
	if p == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan not found: " + id))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plan: %s\n", p.Metadata.ID)
	fmt.Fprintf(&sb, "Title: %s\n", p.Metadata.Title)
	fmt.Fprintf(&sb, "Status: %s\n", p.Metadata.Status)
	fmt.Fprintf(&sb, "Directory: %s\n", p.Metadata.WorkingDirectory)
	fmt.Fprintf(&sb, "Created: %s\n", time.UnixMilli(p.Metadata.CreatedAt).Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&sb, "Updated: %s\n", time.UnixMilli(p.Metadata.UpdatedAt).Format("2006-01-02 15:04:05"))
	if len(p.Steps) > 0 {
		sb.WriteString("\nSteps:\n")
		for _, s := range p.Steps {
			mark := "  "
			if s.Status == "completed" {
				mark = "x "
			}
			fmt.Fprintf(&sb, "  [%s] %d. %s\n", mark, s.Number, s.Description)
		}
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

// ---------------------------------------------------------------------------
// Footer
// ---------------------------------------------------------------------------

func (a *App) planFooter(_ *tui.Model) string {
	switch a.planState {
	case planPlanning:
		return "plan mode (read-only)"
	case planPending:
		choices := []string{"Execute plan", "Cancel"}
		var sb strings.Builder
		for i, c := range choices {
			if i > 0 {
				sb.WriteByte('\n')
			}
			if i == a.planChoice {
				sb.WriteString(tui.ChoiceActiveStyle.Render("❯ " + c))
			} else {
				sb.WriteString(tui.ChoiceInactiveStyle.Render("  " + c))
			}
		}
		return sb.String()
	case planExecuting:
		return fmt.Sprintf("executing plan: %d/%d completed", a.completedCount(), len(a.planSteps))
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (a *App) completedCount() int {
	n := 0
	for _, s := range a.planSteps {
		if s.Completed {
			n++
		}
	}
	return n
}

func (a *App) allStepsCompleted() bool {
	for _, s := range a.planSteps {
		if !s.Completed {
			return false
		}
	}
	return len(a.planSteps) > 0
}

func (a *App) remainingSteps() []planStep {
	var remaining []planStep
	for _, s := range a.planSteps {
		if !s.Completed {
			remaining = append(remaining, s)
		}
	}
	return remaining
}

func (a *App) formatSteps() string {
	var sb strings.Builder
	sb.WriteString("Plan steps:\n")
	for _, s := range a.planSteps {
		mark := "  "
		if s.Completed {
			mark = "x "
		}
		fmt.Fprintf(&sb, "  [%s] %d. %s\n", mark, s.Number, s.Text)
	}
	return sb.String()
}

// toPlanSteps converts in-memory planSteps to persistable plan.Step slice.
func toPlanSteps(steps []planStep) []plan.Step {
	result := make([]plan.Step, len(steps))
	for i, s := range steps {
		status := "pending"
		if s.Completed {
			status = "completed"
		}
		result[i] = plan.Step{Number: s.Number, Description: s.Text, Status: status}
	}
	return result
}

// planTitle derives a plan title from the first step description.
func planTitle(steps []planStep) string {
	if len(steps) == 0 {
		return ""
	}
	title := steps[0].Text
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60]) + "..."
	}
	return title
}
