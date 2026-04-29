package approval

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/voocel/agentcore/permission"
	"github.com/voocel/codebot/internal/config"
)

type Engine struct {
	mu           sync.RWMutex
	cwd          string
	onAudit      func(AuditEntry)
	approver     ApproverFunc
	rules        *RuleSet
	store        *permission.Store
	sessionAllow map[string]storedEntry
	planAllow    []string
	planFilePath string
	tool         decisionEngine
}

// planModeAllowedTools lists Internal-capability tools that may run while
// codebot is in plan mode. Plan mode is read-only by default; these are the
// control-plane tools that drive the plan-mode UX itself, so blocking them
// would make the mode unusable. Listed centrally so the policy is auditable
// in one place rather than scattered across each tool's metadata.
var planModeAllowedTools = []string{
	"exit_plan_mode", // submits the plan for review — exit point of plan mode
	"ask_user",       // structured clarification — needed mid-planning
	"tool_search",    // schema discovery for deferred tools — pure inspection
}

// planModeAllowExec decides whether an exec-capability tool may run during
// plan mode. The plan-mode prompt instructs the model to use bash strictly
// for read-only exploration (grep / find / git status / cat / ...) and to
// avoid any write commands; we trust that contract here rather than parsing
// shell syntax. Approval mode still applies once the plan is approved — at
// that point the harness leaves plan mode and the regular ask flow returns.
func planModeAllowExec(req permission.Request) bool {
	return req.ToolName == "bash"
}

func NewEngine(cwd string, mode Mode, rules *RuleSet, onAudit func(AuditEntry)) (*Engine, error) {
	store, err := permission.NewStore(config.ApprovalsPath(cwd))
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cwd:          cwd,
		onAudit:      onAudit,
		store:        store,
		rules:        rules,
		sessionAllow: make(map[string]storedEntry),
	}
	e.tool = permission.NewEngine(permission.EngineConfig{
		Workspace:            cwd,
		Mode:                 permission.Mode(mode),
		Rules:                (*permission.RuleSet)(rules),
		Store:                store,
		OnAudit:              onAudit,
		PlanModeAllowedTools: planModeAllowedTools,
		PlanModeExecAllowed:  planModeAllowExec,
		PlanModeWriteAllowed: e.planModeAllowWrite,
	})
	return e, nil
}

// planModeAllowWrite permits write/edit only when targeting the registered
// plan file. The plan Manager updates this on Enter/Approve/Cancel via
// SetPlanFilePath; an empty path disables the allowance.
func (e *Engine) planModeAllowWrite(req permission.Request) bool {
	e.mu.RLock()
	target := e.planFilePath
	e.mu.RUnlock()
	if target == "" {
		return false
	}
	if req.ToolName != "write" && req.ToolName != "edit" {
		return false
	}
	var args struct {
		Path string `json:"path"`
	}
	if len(req.Args) == 0 {
		return false
	}
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return false
	}
	path := args.Path
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) && e.cwd != "" {
		path = filepath.Join(e.cwd, path)
	}
	return filepath.Clean(path) == filepath.Clean(target)
}

func (e *Engine) SetPlanFilePath(path string) {
	e.mu.Lock()
	e.planFilePath = path
	e.mu.Unlock()
}

func (e *Engine) PlanFilePath() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.planFilePath
}

func (e *Engine) Decide(ctx context.Context, req permission.Request) (*permission.Decision, error) {
	if decision := e.decideApprovedPlanAction(req); decision != nil {
		return decision, nil
	}
	return e.tool.Decide(ctx, req)
}

func (e *Engine) SetFilesystemRoots(roots FilesystemRoots) {
	e.tool.SetFilesystemRoots(permission.FilesystemRoots(roots))
}

func (e *Engine) Mode() Mode {
	return Mode(e.tool.Mode())
}

func (e *Engine) SetMode(mode Mode) {
	e.tool.SetMode(permission.Mode(mode))
}

func (e *Engine) PlanMode() bool {
	return e.tool.PlanMode()
}

func (e *Engine) SetPlanMode(active bool) {
	e.tool.SetPlanMode(active)
}

func (e *Engine) SetSkillAllows(rawTools []string) {
	e.tool.SetSkillAllows(rawTools)
}

func (e *Engine) SetApprover(fn ApproverFunc) {
	e.mu.Lock()
	e.approver = fn
	e.mu.Unlock()
	e.tool.SetApprover(permission.Approver(fn))
}

func (e *Engine) ApproveHook(ctx context.Context, req HookRequest) error {
	info := inspectHook(req)
	mode, planMode := e.Mode(), e.PlanMode()

	if planMode {
		reason := "plan mode is read-only"
		e.audit(info, mode, planMode, "deny", false, reason)
		return errors.New(reason)
	}

	if e.allowed(info.key) || e.allowed("session:"+string(info.capability)) {
		e.audit(info, mode, planMode, "allow", true, "allowed by stored approval")
		return nil
	}

	switch mode {
	case ModeTrust:
		e.audit(info, mode, planMode, "allow", true, "")
		return nil
	case ModeStrict:
		reason := "strict mode denies this capability"
		e.audit(info, mode, planMode, "deny", false, reason)
		return errors.New(reason)
	}

	choice, err := e.prompt(ctx, info)
	if err != nil {
		return err
	}
	if choice == ChoiceDeny {
		reason := firstNonEmpty(info.reason, "hook denied")
		e.audit(info, mode, planMode, "deny", false, reason)
		return errors.New(reason)
	}
	e.rememberChoice(info, choice)
	e.audit(info, mode, planMode, string(choice), true, "")
	return nil
}

func (e *Engine) ApproveCommand(_ context.Context, req CommandRequest) error {
	req.Category = normalizeCommandCategory(req.Category)
	info := inspectCommand(req)
	mode, planMode := e.Mode(), e.PlanMode()

	if req.NeedsIdle && req.IsRunning {
		reason := "command requires idle agent; press Esc to abort current run"
		e.audit(info, mode, planMode, "deny", false, reason)
		return errors.New(reason)
	}

	if planMode {
		switch req.Category {
		case CommandCategoryInfo, CommandCategoryPlan, CommandCategoryExit:
			e.audit(info, mode, planMode, "allow", true, "")
			return nil
		default:
			reason := "command is unavailable in plan mode"
			e.audit(info, mode, planMode, "deny", false, reason)
			return errors.New(reason)
		}
	}

	e.audit(info, mode, planMode, "allow", true, "")
	return nil
}

func (e *Engine) prompt(ctx context.Context, info toolInfo) (Choice, error) {
	e.mu.RLock()
	approver := e.approver
	e.mu.RUnlock()
	if approver == nil {
		return ChoiceDeny, nil
	}
	return approver(ctx, Prompt{
		Tool:       info.tool,
		Summary:    info.summary,
		Reason:     info.reason,
		Capability: permission.Capability(info.capability),
		Preview:    info.preview,
	})
}

func (e *Engine) rememberChoice(info toolInfo, choice Choice) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch choice {
	case ChoiceAllowAlways:
		entry := storedEntry{
			Key:        info.key,
			Tool:       info.tool,
			Capability: permission.Capability(info.capability),
			Summary:    info.summary,
			AddedAt:    time.Now(),
		}
		e.sessionAllow[info.key] = entry
		if e.store != nil {
			_ = e.store.Add(entry)
		}
	case ChoiceAllowSession:
		key := "session:" + string(info.capability)
		e.sessionAllow[key] = storedEntry{
			Key:        key,
			Tool:       info.tool,
			Capability: permission.Capability(info.capability),
			Summary:    info.summary,
			AddedAt:    time.Now(),
		}
	}
}

func (e *Engine) allowed(key string) bool {
	if key == "" {
		return false
	}

	e.mu.RLock()
	if entry, ok := e.sessionAllow[key]; ok && entry.Key != "" {
		e.mu.RUnlock()
		return true
	}
	e.mu.RUnlock()

	return e.store != nil && e.store.Has(key)
}

func (e *Engine) audit(info toolInfo, mode Mode, planMode bool, decision string, allow bool, reason string) {
	if e.onAudit == nil {
		return
	}
	e.onAudit(AuditEntry{
		Time:       time.Now(),
		Mode:       mode,
		PlanMode:   planMode,
		Tool:       info.tool,
		Capability: info.capability,
		Summary:    info.summary,
		Decision:   decision,
		Reason:     reason,
		Allow:      allow,
	})
}
