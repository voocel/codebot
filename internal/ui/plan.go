package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type planState int

const (
	planOff      planState = iota
	planPlanning           // read-only exploration, agent writes plan
	planReview             // plan submitted, awaiting user approval
)

// ---------------------------------------------------------------------------
// System prompts
// ---------------------------------------------------------------------------

const planModePrompt = `[PLAN MODE - Read-Only]
You are in plan mode. Explore and analyze the codebase, then create a detailed implementation plan.

IMPORTANT: When your plan is ready, you MUST:
1. Write the FULL plan as text in the conversation (so the user can read it)
2. Then call exit_plan_mode with both title and content parameters

Do NOT call exit_plan_mode before writing the plan. Do NOT modify any files.`

func buildPlanContextSuffix(title, content string) string {
	return fmt.Sprintf("[APPROVED PLAN]\nExecute the following plan.\n\nPlan: %s\n\n%s", title, content)
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func (a *App) cmdPlan(args []string) tea.Cmd {
	if len(args) == 0 {
		switch a.planState {
		case planOff:
			return a.enterPlanMode("")
		case planPlanning:
			return tui.SendCommandResult(tui.CommandStyle.Render(
				"Already in plan mode (read-only). Use /plan cancel to abort."))
		case planReview:
			return tui.SendCommandResult(tui.CommandStyle.Render(
				"Plan awaiting approval. Use arrow keys to select, Enter to confirm."))
		}
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "cancel":
		return a.cancelPlanMode()
	default:
		if a.planState != planOff {
			return tui.SendCommandResult(tui.ErrorStyle.Render(
				"Already in plan mode. Use /plan cancel to exit first."))
		}
		return a.enterPlanMode(strings.Join(args, " "))
	}
}

// ---------------------------------------------------------------------------
// State transitions
// ---------------------------------------------------------------------------

// enterPlanMode is the shared setup for both /plan command and enter_plan_mode tool.
func (a *App) enterPlanMode(task string) tea.Cmd {
	readOnly := a.Session.ToolsByName("read", "find", "grep", "ls", "bash")
	a.Session.SetTools(append(readOnly, newExitPlanModeTool())...)
	a.Session.SetSystemSuffix(planModePrompt)
	a.planState = planPlanning
	a.planContent = ""
	a.planTitle = ""

	prompt := "You are now in plan mode. Explore the codebase and write a detailed implementation plan.\nWrite your complete plan as text, then call exit_plan_mode with the title and content."
	if task != "" {
		prompt += "\n\nTask: " + task
	}
	return a.sendAsPrompt(prompt)
}

func (a *App) executePlan() tea.Cmd {
	if a.planState != planReview {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No plan to execute."))
	}

	title, content := a.planTitle, a.planContent

	a.Session.RestoreAllTools(newEnterPlanModeTool())
	a.Session.SetSystemSuffix(buildPlanContextSuffix(title, content))

	a.planState = planOff
	a.planContent = ""
	a.planTitle = ""

	return a.sendAsPrompt("The plan has been approved. Execute it now.")
}

func (a *App) editPlan() tea.Cmd {
	if a.planState != planReview {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No plan to edit."))
	}

	readOnly := a.Session.ToolsByName("read", "find", "grep", "ls", "bash")
	a.Session.SetTools(append(readOnly, newExitPlanModeTool())...)
	a.Session.SetSystemSuffix(planModePrompt)
	a.planState = planPlanning
	a.planContent = ""
	a.planTitle = ""

	return tui.SendCommandResult(tui.CommandStyle.Render(
		"Back to plan mode. Type your feedback to revise the plan."))
}

func (a *App) cancelPlanMode() tea.Cmd {
	if a.planState == planOff {
		return tui.SendCommandResult(tui.CommandStyle.Render("Not in plan mode."))
	}
	a.resetPlanState()
	return tui.SendCommandResult(tui.CommandStyle.Render("Plan mode cancelled. All tools restored."))
}

func (a *App) resetPlanState() {
	a.Session.RestoreAllTools(newEnterPlanModeTool())
	a.Session.SetSystemSuffix("")
	a.planState = planOff
	a.planContent = ""
	a.planTitle = ""
}

// ---------------------------------------------------------------------------
// Event handling
// ---------------------------------------------------------------------------

func (a *App) planOnEvent(_ *tui.Model, ev agentcore.Event) tea.Cmd {
	switch ev.Type {
	case agentcore.EventToolExecEnd:
		if ev.IsError {
			return nil
		}
		switch ev.Tool {
		case "enter_plan_mode":
			return a.onEnterPlanMode()
		case "exit_plan_mode":
			return a.onExitPlanMode(ev.Result)
		}
	case agentcore.EventAgentEnd:
		// Show plan box and approval menu after agent fully stops.
		if a.planState == planReview {
			title := tui.ChoiceActiveStyle.Render("Plan: " + a.planTitle)
			hint := tui.MutedStyle.Render("Select an action below.")
			box := tui.PlanBoxStyle.Render(title + "\n" + hint)
			return tea.Println("\n" + box)
		}
	}
	return nil
}

// onEnterPlanMode handles LLM-initiated plan mode entry.
func (a *App) onEnterPlanMode() tea.Cmd {
	if a.planState != planOff {
		return nil
	}
	readOnly := a.Session.ToolsByName("read", "find", "grep", "ls", "bash")
	a.Session.SetTools(append(readOnly, newExitPlanModeTool())...)
	a.Session.SetSystemSuffix(planModePrompt)
	a.planState = planPlanning
	a.planContent = ""
	a.planTitle = ""
	return nil
}

// onExitPlanMode captures plan content when LLM signals completion.
func (a *App) onExitPlanMode(result json.RawMessage) tea.Cmd {
	if a.planState != planPlanning {
		return nil
	}

	// Extract plan content and title from tool arguments.
	var resp struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(result, &resp)

	// Primary source: tool argument. Fallback: last assistant text.
	a.planContent = resp.Content
	if a.planContent == "" {
		a.planContent = a.Session.LastAssistantText()
	}

	if resp.Title != "" {
		a.planTitle = resp.Title
	} else {
		a.planTitle = extractTitle(a.planContent)
	}

	// Archive to disk (fire-and-forget, no state dependency).
	if a.PlanStore != nil {
		_ = a.PlanStore.Save(storage.GenerateName(), a.planContent)
	}

	a.planState = planReview
	a.planChoice = 0

	// Stop the agent so LLM doesn't get another turn.
	// Safe: tool_result is written to messages AFTER executeToolCalls returns
	// (loop.go:167-173), then ctx.Err() check (loop.go:102-106) exits cleanly.
	a.Session.Abort()

	return nil
}

// extractTitle returns the first markdown heading or first non-empty line.
func extractTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
		if len([]rune(line)) > 60 {
			return string([]rune(line)[:57]) + "..."
		}
		return line
	}
	return "(untitled)"
}

// ---------------------------------------------------------------------------
// Footer
// ---------------------------------------------------------------------------

func (a *App) planFooter(m *tui.Model) string {
	switch a.planState {
	case planPlanning:
		return "plan mode (read-only)"
	case planReview:
		if m.Running {
			return "plan mode (submitting...)"
		}
		choices := []string{"Execute plan", "Edit plan", "Cancel"}
		var sb strings.Builder
		for i, c := range choices {
			if i > 0 {
				sb.WriteByte('\n')
			}
			if i == a.planChoice {
				sb.WriteString(tui.ChoiceActiveStyle.Render("> " + c))
			} else {
				sb.WriteString(tui.ChoiceInactiveStyle.Render("  " + c))
			}
		}
		return sb.String()
	default:
		return ""
	}
}
