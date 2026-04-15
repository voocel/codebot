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

const modePrompt = `[PLAN MODE - Read-Only]
You are in plan mode. Explore and analyze the codebase, then create a detailed implementation plan.

IMPORTANT: When your plan is ready, you MUST:
1. Write the FULL plan as text in the conversation
2. Then call exit_plan_mode with the title, content, and any allowed_commands

Do NOT call exit_plan_mode before writing the plan. Do NOT modify any files.`

type Manager struct {
	session      *agent.Session
	approval     *approval.Engine
	planStore    *storage.PlanStore
	sessionStore *storage.Store
	state        *Store
}

func NewManager(session *agent.Session, approvalEngine *approval.Engine, planStore *storage.PlanStore, sessionStore *storage.Store) *Manager {
	return &Manager{
		session:      session,
		approval:     approvalEngine,
		planStore:    planStore,
		sessionStore: sessionStore,
		state:        NewStore(),
	}
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

func (c *Manager) Enter(task string) (string, error) {
	state := c.state.Snapshot()
	if state.Phase != PhaseOff {
		return "", fmt.Errorf("plan mode already active")
	}

	slug := storage.GenerateName()
	if err := c.ensurePlanFile(slug, task); err != nil {
		return "", err
	}

	preMode := ""
	if c.approval != nil {
		preMode = string(c.approval.Mode())
	}

	next := State{
		Phase:   PhasePlanning,
		Task:    strings.TrimSpace(task),
		Slug:    slug,
		PreMode: preMode,
	}
	c.state.Replace(next)
	c.applyState(next)
	if err := c.persist(next); err != nil {
		return "", err
	}

	prompt := "You are now in plan mode. Explore the codebase and write a detailed implementation plan.\nWrite your complete plan as text, then call exit_plan_mode with the title, content, and any allowed command prefixes."
	if next.Task != "" {
		prompt += "\n\nTask: " + next.Task
	}
	return prompt, nil
}

func (c *Manager) Submit(title, content string, commands []AllowedCommand) error {
	state := c.state.Snapshot()
	if state.Phase != PhasePlanning {
		return fmt.Errorf("exit_plan_mode is only available while planning")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("plan content is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = ExtractTitle(content)
	}
	if err := c.planStore.Save(state.Slug, content); err != nil {
		return err
	}
	next := state
	next.Phase = PhaseReview
	next.Title = title
	next.AllowedCommands = append([]AllowedCommand(nil), commands...)
	c.state.Replace(next)
	if err := c.persist(next); err != nil {
		return err
	}
	return nil
}

func (c *Manager) Approve() (string, string, []AllowedCommand, error) {
	state := c.state.Snapshot()
	if state.Phase != PhaseReview {
		return "", "", nil, fmt.Errorf("no plan awaiting approval")
	}
	content, err := c.planStore.Load(state.Slug)
	if err != nil {
		return "", "", nil, err
	}
	if strings.TrimSpace(content) == "" {
		return "", "", nil, fmt.Errorf("plan file is empty")
	}

	next := state
	next.Phase = PhaseOff
	next.Task = ""
	c.state.Replace(next)
	c.applyState(next)
	if err := c.persist(next); err != nil {
		return "", "", nil, err
	}
	return next.Title, content, append([]AllowedCommand(nil), next.AllowedCommands...), nil
}

func (c *Manager) Revise(feedback string) (string, error) {
	state := c.state.Snapshot()
	if state.Phase != PhaseReview {
		return "", fmt.Errorf("no plan to revise")
	}
	next := state
	next.Phase = PhasePlanning
	next.Title = ""
	next.Task = ""
	next.AllowedCommands = nil
	c.state.Replace(next)
	c.applyState(next)
	if err := c.persist(next); err != nil {
		return "", err
	}
	return "User feedback on the plan: " + strings.TrimSpace(feedback) + "\n\nPlease revise the plan accordingly.", nil
}

func (c *Manager) Cancel() error {
	state := c.state.Snapshot()
	if state.Phase == PhaseOff {
		return nil
	}
	next := State{
		Phase:   PhaseOff,
		PreMode: state.PreMode,
	}
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

	const (
		planModeOverlay     = "plan.mode"
		planApprovedOverlay = "plan.approved"
	)

	switch state.Phase {
	case PhasePlanning:
		readOnly := c.session.ToolsByName("read", "glob", "grep", "ls", "ask_user")
		c.session.SetTools(append(readOnly, c.newExitTool())...)
		c.session.OverlayPrompt(planModeOverlay, modePrompt)
		c.session.OverlayPrompt(planApprovedOverlay, "")
		if c.approval != nil {
			c.approval.SetPlanMode(true)
			c.approval.SetPlanAllowedCommands(nil)
		}
	case PhaseReview:
		c.session.OverlayPrompt(planModeOverlay, modePrompt)
		c.session.OverlayPrompt(planApprovedOverlay, "")
		if c.approval != nil {
			c.approval.SetPlanMode(true)
			c.approval.SetPlanAllowedCommands(nil)
		}
	default:
		c.session.RestoreAllTools()
		c.session.OverlayPrompt(planModeOverlay, "")
		if c.approval != nil {
			c.approval.SetPlanMode(false)
			if mode, err := approval.ParseMode(state.PreMode); err == nil && state.PreMode != "" {
				c.approval.SetMode(mode)
			}
			c.approval.SetPlanAllowedCommands(AllowedCommandPrefixes(state.AllowedCommands))
		}
		if state.Slug != "" && state.Title != "" && c.planStore != nil {
			if content, err := c.planStore.Load(state.Slug); err == nil && strings.TrimSpace(content) != "" {
				c.session.OverlayPrompt(planApprovedOverlay, BuildApprovedPlanPrompt(state.Title, content, state.AllowedCommands))
				return
			}
		}
		c.session.OverlayPrompt(planApprovedOverlay, "")
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
		}
	}
	for _, tool := range c.session.ToolsByName("exit_plan_mode") {
		if exitTool, ok := tool.(*localtools.ExitPlanModeTool); ok {
			exitTool.SetValidator(func() error {
				if c.state.Snapshot().Phase != PhasePlanning {
					return fmt.Errorf("exit_plan_mode is only available while planning")
				}
				return nil
			})
		}
	}
}

func (c *Manager) newEnterTool() *localtools.EnterPlanModeTool {
	tool := localtools.NewEnterPlanMode()
	tool.SetValidator(func() error {
		if c.state.Snapshot().Phase != PhaseOff {
			return fmt.Errorf("plan mode already active")
		}
		return nil
	})
	return tool
}

func (c *Manager) newExitTool() *localtools.ExitPlanModeTool {
	tool := localtools.NewExitPlanMode()
	tool.SetValidator(func() error {
		if c.state.Snapshot().Phase != PhasePlanning {
			return fmt.Errorf("exit_plan_mode is only available while planning")
		}
		return nil
	})
	return tool
}

func (c *Manager) persist(state State) error {
	if c.sessionStore == nil {
		return nil
	}
	return c.sessionStore.AppendPlanState(
		string(state.Phase),
		state.Slug,
		state.Title,
		state.PreMode,
		AllowedCommandsToEntries(state.AllowedCommands),
	)
}

func (c *Manager) ensurePlanFile(slug, task string) error {
	if c.planStore == nil || slug == "" {
		return nil
	}
	path := c.planStore.Path(slug)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	content := "# Plan\n"
	if strings.TrimSpace(task) != "" {
		content += "\nTask: " + strings.TrimSpace(task) + "\n"
	}
	content += "\n## Draft\n"
	return c.planStore.Save(slug, content)
}

func BuildApprovedPlanPrompt(title, content string, commands []AllowedCommand) string {
	var b strings.Builder
	b.WriteString("[APPROVED PLAN]\nExecute the following plan.\n\n")
	b.WriteString("Plan: " + title + "\n")
	if len(commands) > 0 {
		b.WriteString("\nAllowed command prefixes for this session:\n")
		for _, command := range commands {
			b.WriteString("- " + describeAllowedCommandForPrompt(command) + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(content)
	return b.String()
}

func describeAllowedCommandForPrompt(command AllowedCommand) string {
	desc := strings.TrimSpace(command.Description)
	if desc == "" || desc == command.CommandPrefix {
		return command.CommandPrefix
	}
	return command.CommandPrefix + " (" + desc + ")"
}

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
