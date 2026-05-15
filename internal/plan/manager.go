package plan

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

// buildPlanModeContract produces the plan-mode behavioral contract: the
// MUST-NOT rules, the writable plan file path, and the end-of-turn obligation.
// This is the *constraint* slice of plan-mode instructions — the part that
// must be salient on every model turn. The complementary *guidance* slice
// (iterative loop, asking-good-questions, plan-file-structure, etc.) is
// delivered once as the first plan-mode system-reminder by
// agent.planModeReminderForNextPrompt; re-reading workflow tips on every
// re-entry has no salience benefit and just burns tokens. See plan.Manager.Enter
// and agent/plan_reminders.go for how the two slices flow into the model.
//
// Wording mirrors CC's iterative plan-mode instructions
// (claude-code-src/utils/messages.ts:getPlanModeInterviewInstructions) so the
// model gets the same MUST-NOT framing and end-of-turn contract.
func buildPlanModeContract(planFilePath string) string {
	return `Plan mode is active. The user indicated that they do not want you to execute yet — you MUST NOT make any edits (with the exception of the plan file mentioned below), run any non-readonly tools (including changing configs or making commits), or otherwise make any changes to the system. This supercedes any other instructions you have received.

## Plan File Info:
A plan file exists at ` + planFilePath + `. Use the write tool to populate it (if empty) or the edit tool to refine it incrementally as your understanding grows. NOTE that this is the only file you are allowed to edit — other than this you are only allowed to take READ-ONLY actions.

## Ending Your Turn

Your turn should only end by either:
- Using ask_user to gather more information
- Calling exit_plan_mode when the plan is ready for approval

**Important:** Use exit_plan_mode to request plan approval. Do NOT ask about plan approval via text or ask_user. Phrases like "Is this plan okay?", "Should I proceed?", "How does this plan look?" MUST go through exit_plan_mode.

Detailed planning workflow guidance arrives in the next system-reminder.`
}

type Manager struct {
	session      *agent.Session
	approval     *approval.Engine
	planStore    *storage.PlanStore
	sessionStore *storage.Store
	state        *Store

	// cancelPending is a one-shot flag set by Cancel() and consumed on the
	// next signal() poll. Mirrors CC's needsPlanModeExitAttachment: when the
	// user aborts plan mode via /plan cancel there is no tool_result to
	// carry an "exit signal" into history, so we inject a one-time reminder
	// telling the model the read-only contract from the EnterPlanMode
	// tool_result no longer applies. Intentionally process-local (not
	// persisted): a restart after cancellation is itself a clean break.
	cancelPending atomic.Bool
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

// signal answers the runtime poll before every user prompt. Three possible
// shapes (see agent.PlanModeSignal). Cancel-pending wins over off; active is
// mutually exclusive with both.
func (c *Manager) signal() agent.PlanModeSignal {
	state := c.state.Snapshot()
	if state.Phase == PhasePlanning && c.planStore != nil {
		return agent.PlanModeSignal{Active: true, PlanFilePath: c.planStore.Path(state.Slug)}
	}
	if c.cancelPending.CompareAndSwap(true, false) {
		return agent.PlanModeSignal{JustCancelled: true}
	}
	return agent.PlanModeSignal{}
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
	// Drop any pending cancel reminder — we're re-entering plan mode before
	// it could fire, so the "you have exited plan mode" signal would be a lie.
	c.cancelPending.Store(false)

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
	// Return only the contract (MUST-NOT + plan path + end-of-turn rules).
	// The complementary workflow guidance is delivered as the first
	// plan-mode system-reminder by agent.planModeReminderForNextPrompt on
	// the next user prompt. Splitting the two slices keeps the per-Enter
	// tool_result small (~200 tokens vs ~950) so re-entering plan mode in
	// the same session doesn't pile up duplicate guidance in history.
	// System prompt (SB1/SB2/SB3) stays byte-stable across plan toggles —
	// see Step 7 perf: optimize prompt cache and the applyState comment.
	return buildPlanModeContract(planPath), nil
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
// plan file to exist. Sets cancelPending so the next user prompt picks up a
// one-shot "you have exited plan mode" reminder — exit_plan_mode carries an
// equivalent signal in its tool_result, but /plan cancel has no tool_result
// to ride on, so the reminder pipeline closes that gap.
func (c *Manager) Cancel() error {
	state := c.state.Snapshot()
	if state.Phase == PhaseOff {
		return nil
	}
	next := State{Phase: PhaseOff, PreMode: state.PreMode}
	c.state.Replace(next)
	c.applyState(next)
	c.cancelPending.Store(true)
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

	// Plan mode instructions are NOT written into the system prompt anymore.
	// Mutating SB3 on every enter/exit would invalidate the marker#3 prefix
	// and force re-write of tools+history (Step 7). Instead the rules flow
	// in via two channels:
	//   1. Contract (MUST-NOT + plan path + end-of-turn) → Enter() return
	//      value, surfaces as the enter_plan_mode tool_result (or as the
	//      slash-command user message). Small (~200 tokens), repeats per
	//      Enter but cheap to duplicate.
	//   2. Workflow guidance (explore loop, asking-good-questions, plan
	//      structure, when-to-converge) → first plan-mode system-reminder
	//      injected by plan_reminders.go on the next user prompt, then
	//      sparse refresh on a 5-turn cadence.
	// The system prompt stays byte-stable across plan toggles, so SB cache
	// entries survive intact.
	switch state.Phase {
	case PhasePlanning:
		if c.approval != nil {
			c.approval.SetPlanMode(true)
			c.approval.SetPlanContentProvider(c.CurrentPlan)
		}
	default:
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
