package plan

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

// buildModePrompt produces the plan-mode system overlay. The plan file is the
// canonical artifact: the model edits it incrementally with write/edit; the
// permission engine denies all other writes (and Subagent dispatch) while
// PlanMode=true. exit_plan_mode signals readiness and routes through the
// permission ask flow for user approval.
//
// Wording mirrors CC's iterative plan-mode instructions
// (claude-code-src/utils/messages.ts:getPlanModeInterviewInstructions) so the
// model gets the same MUST-NOT framing, the explore→update→ask loop, and the
// strict end-of-turn contract.
func buildModePrompt(planFilePath string) string {
	return `Plan mode is active. The user indicated that they do not want you to execute yet — you MUST NOT make any edits (with the exception of the plan file mentioned below), run any non-readonly tools (including changing configs or making commits), or otherwise make any changes to the system. This supercedes any other instructions you have received.

## Plan File Info:
A plan file exists at ` + planFilePath + `. Use the write tool to populate it (if empty) or the edit tool to refine it incrementally as your understanding grows.
You should build your plan incrementally by writing to or editing this file. NOTE that this is the only file you are allowed to edit — other than this you are only allowed to take READ-ONLY actions.

## Iterative Planning Workflow

You are pair-planning with the user. Explore the code to build context, ask the user questions when you hit decisions you can't make alone, and write your findings into the plan file as you go. The plan file (above) is the ONLY file you may edit — it starts as a rough skeleton and gradually becomes the final plan.

### The Loop

Repeat this cycle until the plan is complete:

1. **Explore** — Use read, grep, glob, ls (and bash for read-only commands like ` + "`git status`" + `, ` + "`find`" + `, ` + "`cat`" + `, ` + "`sed -n`" + `) to read code. Look for existing functions, utilities, and patterns to reuse.
2. **Update the plan file** — After each discovery, immediately capture what you learned. Don't wait until the end.
3. **Ask the user** — When you hit an ambiguity or decision you can't resolve from code alone, use ask_user. Then go back to step 1.

### First Turn

Start by quickly scanning a few key files to form an initial understanding of the task scope. Then write a skeleton plan (headers and rough notes) and ask the user your first round of questions. Don't explore exhaustively before engaging the user.

### Asking Good Questions

- Never ask what you could find out by reading the code
- Batch related questions together (single ask_user call with multiple questions when applicable)
- Focus on things only the user can answer: requirements, preferences, tradeoffs, edge case priorities
- Scale depth to the task — a vague feature request needs many rounds; a focused bug fix may need one or none

### Plan File Structure

Your plan file should be divided into clear sections using markdown headers, based on the request. Fill out these sections as you go.
- Begin with a **Context** section: explain why this change is being made — the problem or need it addresses, what prompted it, and the intended outcome
- Include only your recommended approach, not all alternatives
- Ensure that the plan file is concise enough to scan quickly, but detailed enough to execute effectively
- Include the paths of critical files to be modified
- Reference existing functions and utilities you found that should be reused, with their file paths
- Include a verification section describing how to test the changes end-to-end (run the code, run tests, manual checks)

### When to Converge

Your plan is ready when you've addressed all ambiguities and it covers: what to change, which files to modify, what existing code to reuse (with file paths), and how to verify the changes. Call exit_plan_mode when the plan is ready for approval.

### Ending Your Turn

Your turn should only end by either:
- Using ask_user to gather more information
- Calling exit_plan_mode when the plan is ready for approval

**Important:** Use exit_plan_mode to request plan approval. Do NOT ask about plan approval via text or ask_user. Phrases like "Is this plan okay?", "Should I proceed?", "How does this plan look?" MUST go through exit_plan_mode.`
}

type Manager struct {
	session      *agent.Session
	approval     *approval.Engine
	planStore    *storage.PlanStore
	sessionStore *storage.Store
	state        *Store
}

func NewManager(session *agent.Session, approvalEngine *approval.Engine, planStore *storage.PlanStore, sessionStore *storage.Store) *Manager {
	m := &Manager{
		session:      session,
		approval:     approvalEngine,
		planStore:    planStore,
		sessionStore: sessionStore,
		state:        NewStore(),
	}
	if session != nil {
		// Drives the periodic plan-mode reminder injection (see
		// agent/plan_reminders.go) — runtime polls before each user prompt.
		session.SetPlanModeSignal(m.signal)
	}
	return m
}

func (c *Manager) signal() (bool, string) {
	state := c.state.Snapshot()
	if state.Phase != PhasePlanning || c.planStore == nil {
		return false, ""
	}
	return true, c.planStore.Path(state.Slug)
}

func (c *Manager) Snapshot() State {
	return c.state.Snapshot()
}

func (c *Manager) Restore(state State) error {
	if state.Phase == "" {
		state.Phase = PhaseOff
	}
	c.state.Replace(state)
	c.applyState(c.state.Snapshot())
	return nil
}

// Enter transitions from PhaseOff to PhasePlanning, generating a fresh plan
// file slug and writing the plan-mode overlay prompt into the session.
func (c *Manager) Enter() (string, error) {
	state := c.state.Snapshot()
	if state.Phase != PhaseOff {
		return "", fmt.Errorf("plan mode already active")
	}

	slug := storage.GenerateName()
	if err := c.ensurePlanFile(slug); err != nil {
		return "", err
	}

	preMode := ""
	if c.approval != nil {
		preMode = string(c.approval.Mode())
	}

	next := State{
		Phase:   PhasePlanning,
		Slug:    slug,
		PreMode: preMode,
	}
	c.state.Replace(next)
	c.applyState(next)
	if err := c.persist(next); err != nil {
		return "", err
	}

	planPath := c.planStore.Path(slug)
	return "Entered plan mode. Build your plan in " + planPath + " using the write or edit tool, then call exit_plan_mode for approval.", nil
}

// Exit unconditionally transitions out of plan mode and returns the plan
// content. User approval is enforced upstream by approval.Engine.Decide
// (matching CC's checkPermissions:'ask' design): when this method is reached
// the user has already approved or the call is from /plan cancel.
func (c *Manager) Exit() (string, error) {
	state := c.state.Snapshot()
	if state.Phase != PhasePlanning {
		return "", fmt.Errorf("not in plan mode")
	}
	content, err := c.planStore.Load(state.Slug)
	if err != nil {
		return "", err
	}

	next := State{Phase: PhaseOff, PreMode: state.PreMode}
	c.state.Replace(next)
	c.applyState(next)
	if err := c.persist(next); err != nil {
		return "", err
	}
	return content, nil
}

// Cancel is invoked from /plan cancel: ends plan mode without expecting any
// plan file to exist.
func (c *Manager) Cancel() error {
	state := c.state.Snapshot()
	if state.Phase == PhaseOff {
		return nil
	}
	next := State{Phase: PhaseOff, PreMode: state.PreMode}
	c.state.Replace(next)
	c.applyState(next)
	return c.persist(next)
}

func (c *Manager) CurrentPlanPath() string {
	state := c.state.Snapshot()
	if state.Slug == "" || c.planStore == nil {
		return ""
	}
	return c.planStore.Path(state.Slug)
}

func (c *Manager) CurrentPlan() (string, error) {
	state := c.state.Snapshot()
	if state.Slug == "" || c.planStore == nil {
		return "", nil
	}
	return c.planStore.Load(state.Slug)
}

func (c *Manager) applyState(state State) {
	if c.session == nil {
		return
	}
	c.wireValidators()

	const planModeOverlay = "plan.mode"

	planPath := ""
	if c.planStore != nil && state.Slug != "" {
		planPath = c.planStore.Path(state.Slug)
	}
	switch state.Phase {
	case PhasePlanning:
		c.session.OverlayPrompt(planModeOverlay, buildModePrompt(planPath))
		if c.approval != nil {
			c.approval.SetPlanMode(true)
			c.approval.SetPlanContentProvider(c.CurrentPlan)
		}
	default:
		c.session.OverlayPrompt(planModeOverlay, "")
		if c.approval != nil {
			c.approval.SetPlanMode(false)
			c.approval.SetPlanContentProvider(nil)
			if mode, err := approval.ParseMode(state.PreMode); err == nil && state.PreMode != "" {
				c.approval.SetMode(mode)
			}
		}
	}
}

func (c *Manager) wireValidators() {
	for _, tool := range c.session.ToolsByName("enter_plan_mode") {
		if enterTool, ok := tool.(*localtools.EnterPlanModeTool); ok {
			enterTool.SetValidator(func() error {
				if c.state.Snapshot().Phase != PhaseOff {
					return fmt.Errorf("plan mode already active")
				}
				return nil
			})
			enterTool.SetHandler(c.Enter)
		}
	}
	for _, tool := range c.session.ToolsByName("exit_plan_mode") {
		if exitTool, ok := tool.(*localtools.ExitPlanModeTool); ok {
			exitTool.SetValidator(func() error {
				if c.state.Snapshot().Phase != PhasePlanning {
					return fmt.Errorf("exit_plan_mode is only available in plan mode")
				}
				return nil
			})
			exitTool.SetExiter(c.Exit)
		}
	}
}

func (c *Manager) persist(state State) error {
	if c.sessionStore == nil {
		return nil
	}
	return c.sessionStore.AppendPlanState(string(state.Phase), state.Slug, state.PreMode)
}

func (c *Manager) ensurePlanFile(slug string) error {
	if c.planStore == nil || slug == "" {
		return nil
	}
	path := c.planStore.Path(slug)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return c.planStore.Save(slug, "# Plan\n")
}

// ExtractTitle pulls the first heading or non-empty line from plan content as
// a display title. Used by the TUI to label review surfaces.
func ExtractTitle(content string) string {
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
