package app

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/tui"
)

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type planState int

const (
	planOff       planState = iota
	planPlanning            // read-only exploration + submit_plan tool
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

Available tools: read, find, grep, ls, bash (read-only commands only), submit_plan
Disabled tools: write, edit

When your plan is ready, call the submit_plan tool with structured steps.
Do NOT write the plan as plain text — you MUST use the submit_plan tool.
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
// Commands
// ---------------------------------------------------------------------------

func (a *App) cmdPlan(args []string) tea.Cmd {
	if len(args) == 0 {
		switch a.planState {
		case planOff:
			return a.enterPlanMode()
		case planPlanning:
			return tui.SendCommandResult(tui.CommandStyle.Render(
				"Already in plan mode (read-only). Use /plan execute or /plan cancel."))
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
	default:
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"Usage: /plan [execute|cancel]"))
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

func (a *App) enterPlanMode() tea.Cmd {
	readOnly := a.Session.ToolsByName("read", "find", "grep", "ls", "bash")
	a.Session.SetTools(append(readOnly, newSubmitPlanTool())...)
	a.Session.SetSystemSuffix(planModePrompt)
	a.planState = planPlanning
	a.planSteps = nil
	return tui.SendCommandResult(tui.CommandStyle.Render(
		"Entered plan mode (read-only). Describe your task — the agent will explore and propose a plan."))
}

func (a *App) executePlan() tea.Cmd {
	if a.planState != planPlanning {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"Not in planning state. Use /plan to enter plan mode first."))
	}
	if len(a.planSteps) == 0 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"No plan steps submitted. Let the agent call submit_plan first."))
	}

	a.Session.RestoreAllTools(newMarkStepTool())
	a.Session.SetSystemSuffix(buildExecutionPrompt(a.planSteps))
	a.planState = planExecuting

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
	a.resetPlanState()
	return tui.SendCommandResult(tui.CommandStyle.Render("Plan mode cancelled. All tools restored."))
}

func (a *App) resetPlanState() {
	a.Session.RestoreAllTools()
	a.Session.SetSystemSuffix("")
	a.planState = planOff
	a.planSteps = nil
}

// ---------------------------------------------------------------------------
// Event handling — driven by tool-use events, no regex parsing
// ---------------------------------------------------------------------------

func (a *App) planOnEvent(_ *tui.Model, ev agentcore.Event) tea.Cmd {
	if ev.Type != agentcore.EventToolExecEnd || ev.IsError {
		return nil
	}

	switch ev.Tool {
	case "submit_plan":
		return a.onSubmitPlan(ev.Result)
	case "mark_step":
		return a.onMarkStep(ev.Result)
	}
	return nil
}

func (a *App) onSubmitPlan(result json.RawMessage) tea.Cmd {
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

	return tea.Println("\n" + tui.CommandStyle.Render(
		a.formatSteps()+"\n\nUse /plan execute to start, /plan cancel to abort."))
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

	if a.allStepsCompleted() {
		a.resetPlanState()
		return tea.Println("\n" + tui.CommandStyle.Render("Plan completed. All steps done. Restored normal mode."))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Footer
// ---------------------------------------------------------------------------

func (a *App) planFooter(_ *tui.Model) string {
	switch a.planState {
	case planPlanning:
		return "plan mode (read-only)"
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
