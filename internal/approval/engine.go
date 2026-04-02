package approval

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/voocel/agentcore/permission"
	"github.com/voocel/codebot/internal/config"
)

type Engine struct {
	mu           sync.RWMutex
	onAudit      func(AuditEntry)
	approver     ApproverFunc
	store        *permission.Store
	sessionAllow map[string]storedEntry
	tool         decisionEngine
}

func NewEngine(cwd string, mode Mode, rules *RuleSet, onAudit func(AuditEntry)) (*Engine, error) {
	store, err := permission.NewStore(config.ApprovalsPath(cwd))
	if err != nil {
		return nil, err
	}
	return &Engine{
		onAudit:      onAudit,
		store:        store,
		sessionAllow: make(map[string]storedEntry),
		tool: permission.NewEngine(permission.EngineConfig{
			Workspace: cwd,
			Mode:      permission.Mode(mode),
			Rules:     (*permission.RuleSet)(rules),
			Store:     store,
			OnAudit:   onAudit,
		}),
	}, nil
}

func (e *Engine) Decide(ctx context.Context, req permission.Request) (*permission.Decision, error) {
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
