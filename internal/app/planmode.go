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
	planOff      planState = iota
	planPlanning           // read-only exploration + submit_plan tool
	planReview             // plan submitted, awaiting user approval
)

// ---------------------------------------------------------------------------
// System prompts
// ---------------------------------------------------------------------------

const planModePrompt = `[PLAN MODE - Read-Only]
You are in plan mode. Explore and analyze the codebase, then create a detailed implementation plan.

Available tools: read, find, grep, ls, bash (read-only commands only), submit_plan
Disabled tools: write, edit

Write your plan as free-form text in your response. When the plan is ready,
call the submit_plan tool to signal completion.
Do NOT modify any files.`

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
	case "list":
		return a.cmdPlanList()
	case "show":
		id := ""
		if len(args) > 1 {
			id = args[1]
		}
		return a.cmdPlanShow(id)
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

func (a *App) enterPlanMode(task string) tea.Cmd {
	readOnly := a.Session.ToolsByName("read", "find", "grep", "ls", "bash")
	a.Session.SetTools(append(readOnly, newSubmitPlanTool())...)
	a.Session.SetSystemSuffix(planModePrompt)
	a.planState = planPlanning
	a.planContent = ""
	a.planTitle = ""

	// Persist draft plan.
	id := plan.GenerateID()
	a.planID = id
	if a.PlanStore != nil {
		now := time.Now().UnixMilli()
		_ = a.PlanStore.Save(&plan.SavedPlan{
			Metadata: plan.Metadata{
				ID:               id,
				Status:           plan.StatusDraft,
				WorkingDirectory: a.Cwd,
				CreatedAt:        now,
				UpdatedAt:        now,
			},
		})
	}

	prompt := "You are now in plan mode. Explore the codebase and write a detailed implementation plan.\nWhen your plan is complete, call the submit_plan tool."
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

	a.Session.RestoreAllTools()
	a.Session.SetSystemSuffix(buildPlanContextSuffix(title, content))

	if a.PlanStore != nil && a.planID != "" {
		_ = a.PlanStore.UpdateStatus(a.planID, plan.StatusCompleted)
	}

	// Return to normal mode. Plan context persists as system suffix
	// and will be cleared by /clear, /new, /resume, or compaction.
	a.planState = planOff
	a.planContent = ""
	a.planTitle = ""
	a.planID = ""

	return a.sendAsPrompt("The plan has been approved. Execute it now.")
}

func (a *App) editPlan() tea.Cmd {
	if a.planState != planReview {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No plan to edit."))
	}

	readOnly := a.Session.ToolsByName("read", "find", "grep", "ls", "bash")
	a.Session.SetTools(append(readOnly, newSubmitPlanTool())...)
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
	if a.PlanStore != nil && a.planID != "" {
		_ = a.PlanStore.UpdateStatus(a.planID, plan.StatusAbandoned)
	}
	a.resetPlanState()
	return tui.SendCommandResult(tui.CommandStyle.Render("Plan mode cancelled. All tools restored."))
}

func (a *App) resetPlanState() {
	a.Session.RestoreAllTools()
	a.Session.SetSystemSuffix("")
	a.planState = planOff
	a.planContent = ""
	a.planTitle = ""
	a.planID = ""
}

// ---------------------------------------------------------------------------
// Event handling
// ---------------------------------------------------------------------------

func (a *App) planOnEvent(_ *tui.Model, ev agentcore.Event) tea.Cmd {
	if ev.Type != agentcore.EventToolExecEnd || ev.IsError {
		return nil
	}
	if ev.Tool == "submit_plan" {
		return a.onSubmitPlan(ev.Result)
	}
	return nil
}

func (a *App) onSubmitPlan(result json.RawMessage) tea.Cmd {
	if a.planState != planPlanning {
		return nil
	}

	// Capture plan content from the LLM's last assistant message.
	a.planContent = a.Session.LastAssistantText()

	// Extract title from tool result.
	var resp struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(result, &resp) == nil && resp.Title != "" {
		a.planTitle = resp.Title
	}

	// Persist.
	if a.PlanStore != nil && a.planID != "" {
		if p, _ := a.PlanStore.Load(a.planID); p != nil {
			p.Content = a.planContent
			p.Metadata.Title = a.planTitle
			p.Metadata.Status = plan.StatusPending
			_ = a.PlanStore.Save(p)
		}
	}

	a.planState = planReview
	a.planChoice = 0
	// Do NOT call Abort() here. EventToolExecEnd fires before tool results
	// are written to conversation history (loop.go:167-172). Aborting would
	// leave a tool_use without a matching tool_result, causing API errors.
	// Instead, let the agent finish the current turn naturally — the tool
	// returned "Waiting for user approval", so the LLM will stop on its own.

	return tea.Println("\n" + tui.CommandStyle.Render(
		fmt.Sprintf("Plan submitted: %s\nSelect an action below.", a.planTitle)))
}

// ---------------------------------------------------------------------------
// Plan list / show
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
		updated := time.UnixMilli(p.Metadata.UpdatedAt).Format("01-02 15:04")
		fmt.Fprintf(&sb, "  %d. [%s] %s  %q  %s\n",
			i+1, p.Metadata.Status, p.Metadata.ID, title, updated)
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (a *App) cmdPlanShow(id string) tea.Cmd {
	if a.PlanStore == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plan store not configured."))
	}
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
	if p.Content != "" {
		sb.WriteString("\n")
		lines := strings.Split(p.Content, "\n")
		for i, line := range lines {
			if i >= 20 {
				sb.WriteString("  ...\n")
				break
			}
			sb.WriteString("  " + line + "\n")
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
	case planReview:
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
