package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
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
	readOnly := a.Session.ToolsByName("read", "glob", "grep", "ls", "ask_user")
	a.Session.SetTools(append(readOnly, localtools.NewExitPlanMode())...)
	a.Session.SetSystemSuffix(planModePrompt)
	if a.ApprovalEngine != nil {
		a.ApprovalEngine.SetMode(approval.ModePlan)
	}
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

	a.Session.RestoreAllTools(localtools.NewEnterPlanMode())
	a.Session.SetSystemSuffix(buildPlanContextSuffix(title, content))
	if a.ApprovalEngine != nil {
		a.ApprovalEngine.SetMode(approval.ModeNormal)
	}

	a.planState = planOff
	a.planContent = ""
	a.planTitle = ""

	return a.sendAsPrompt("The plan has been approved. Execute it now.")
}

func (a *App) cancelPlanMode() tea.Cmd {
	if a.planState == planOff {
		return tui.SendCommandResult(tui.CommandStyle.Render("Not in plan mode."))
	}
	a.resetPlanState()
	return tui.SendCommandResult(tui.CommandStyle.Render("Plan mode cancelled. All tools restored."))
}

func (a *App) resetPlanState() {
	a.Session.RestoreAllTools(localtools.NewEnterPlanMode())
	a.Session.SetSystemSuffix("")
	if a.ApprovalEngine != nil {
		a.ApprovalEngine.SetMode(approval.ModeNormal)
	}
	a.planState = planOff
	a.planContent = ""
	a.planTitle = ""
	a.planOtherMode = false
	a.planOtherBuf = ""
}

// ---------------------------------------------------------------------------
// Plan review keyboard handling (AskUser-style)
// ---------------------------------------------------------------------------

const planOptionCount = 3 // 2 choices + "Type here"

func (a *App) handlePlanReviewKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	// In otherMode, handle typing.
	if a.planOtherMode {
		return a.handlePlanOtherInput(msg)
	}

	// Esc and Ctrl+C cancel plan mode.
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

	// Number shortcuts: 1-3.
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

	// Absorb all other keys.
	return true, nil
}

func (a *App) handlePlanEnter() (bool, tea.Cmd) {
	switch a.planChoice {
	case 0:
		return true, a.executePlan()
	case 1:
		return true, a.cancelPlanMode()
	default:
		// "Type here" option.
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
	if a.planState != planReview {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No plan to edit."))
	}

	readOnly := a.Session.ToolsByName("read", "glob", "grep", "ls", "ask_user")
	a.Session.SetTools(append(readOnly, localtools.NewExitPlanMode())...)
	a.Session.SetSystemSuffix(planModePrompt)
	if a.ApprovalEngine != nil {
		a.ApprovalEngine.SetMode(approval.ModePlan)
	}
	a.planState = planPlanning
	a.planContent = ""
	a.planTitle = ""

	return a.sendAsPrompt("User feedback on the plan: " + feedback + "\n\nPlease revise the plan accordingly.")
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
	readOnly := a.Session.ToolsByName("read", "glob", "grep", "ls", "ask_user")
	a.Session.SetTools(append(readOnly, localtools.NewExitPlanMode())...)
	a.Session.SetSystemSuffix(planModePrompt)
	if a.ApprovalEngine != nil {
		a.ApprovalEngine.SetMode(approval.ModePlan)
	}
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
	a.planOtherMode = false
	a.planOtherBuf = ""

	// Stop the agent so LLM doesn't get another turn.
	// Safe: tool_result is written to messages AFTER executeToolCalls returns
	// (loop.go:167-173), then ctx.Err() check (loop.go:102-106) exits cleanly.
	// AbortSilent: programmatic cancellation — no abort marker in history.
	a.Session.AbortSilent()

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
// Status bar integration
// ---------------------------------------------------------------------------

func (a *App) planStatus(m *tui.Model) *tui.PlanBarInfo {
	switch a.planState {
	case planPlanning:
		return &tui.PlanBarInfo{Tag: "plan mode"}
	case planReview:
		if m.Running {
			return &tui.PlanBarInfo{Tag: "submitting plan..."}
		}
		return &tui.PlanBarInfo{
			Tag:       "plan mode",
			Prompt:    "Would you like to proceed?",
			Choices:   []string{"Execute plan", "Cancel"},
			Active:    a.planChoice,
			OtherMode: a.planOtherMode,
			OtherBuf:  a.planOtherBuf,
		}
	default:
		return nil
	}
}
