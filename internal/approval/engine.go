package approval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore/permission"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/config"
)

type Engine struct {
	mu                  sync.RWMutex
	cwd                 string
	onAudit             func(AuditEntry)
	approver            ApproverFunc
	rules               *RuleSet
	store               *permission.Store
	sessionAllow        map[string]storedEntry
	planContentProvider func() (string, error)
	tool                decisionEngine
}

// planModeAllowedTools lists Internal-capability tools that may run while
// codebot is in plan mode. exit_plan_mode is intentionally NOT here — it
// goes through Engine.Decide's plan-exit interception so the user sees the
// plan content in the standard ask card before the tool runs.
var planModeAllowedTools = []string{
	"ask_user",    // structured clarification — needed mid-planning
	"tool_search", // schema discovery for deferred tools — pure inspection
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
		Classifier:           classify,
		PlanModeAllowedTools: planModeAllowedTools,
		PlanModeExecAllowed:  planModeAllowExec,
	})
	return e, nil
}

// SetPlanContentProvider registers a callback that returns the current plan
// file content. The plan-exit interception in Decide reads from it to surface
// the plan in the approval prompt's preview field. plan.Manager wires this
// on enter / clears it on exit.
func (e *Engine) SetPlanContentProvider(fn func() (string, error)) {
	e.mu.Lock()
	e.planContentProvider = fn
	e.mu.Unlock()
}

// Decide routes a tool permission request. exit_plan_mode in plan mode is
// intercepted here and surfaced through the standard approver path with the
// plan content as preview — ask-style: the tool declares it needs approval
// and the engine handles the prompt.
// Dangerous paths (credential files, shell rc, .git/hooks, IDE/agent loader
// configs, ...) bypass mode auto-pass and stored approvals; the approver is
// invoked with Allow Once / Deny only so a single Allow Always cannot turn
// into a persistent backdoor. Nothing is hard-denied — the model is
// cooperative and the user is in the loop.
//
// All other tools delegate to the agentcore permission engine.
func (e *Engine) Decide(ctx context.Context, req permission.Request) (*permission.Decision, error) {
	// Resolve operand paths against the session's live cwd (a worktree entered
	// mid-turn rides on the run context; see Session.runCtx), falling back to the
	// boot cwd. Keeps the engine's path checks, audit, and CheckDangerousPath
	// aligned with where the tool actually runs.
	workspace := e.cwd
	if live := agentcoretools.CwdFromContext(ctx); live != "" {
		workspace = live
		req.Workspace = live
	}
	if req.ToolName == "exit_plan_mode" && e.PlanMode() {
		return e.decidePlanExit(ctx, req)
	}
	// In plan mode the agentcore engine will deny writes outright; routing
	// the force-ask here would just create a wasted prompt the user couldn't
	// usefully act on.
	if !e.PlanMode() {
		if reason := CheckDangerousPath(workspace, req); reason != "" {
			return e.askDangerousPath(ctx, req, reason)
		}
	}
	return e.tool.Decide(ctx, req)
}

// askDangerousPath routes the request through the approver, ignoring mode
// and stored approvals. Returns DecisionAllowOnce on any allow choice (never
// AllowSession / AllowAlways), matching the OutsideRoots policy at
// agentcore engine.go:286-289.
//
// Headless (no approver wired) falls back to deny — the safe default.
func (e *Engine) askDangerousPath(ctx context.Context, req permission.Request, reason string) (*permission.Decision, error) {
	cap, summary := dangerousPathContext(req)
	info := toolInfo{
		tool:       req.ToolName,
		capability: cap,
		summary:    summary,
		reason:     reason,
	}

	e.mu.RLock()
	approver := e.approver
	e.mu.RUnlock()
	if approver == nil {
		e.audit(info, e.Mode(), e.PlanMode(), "deny", false, "no approver wired")
		return &permission.Decision{
			Kind:       permission.DecisionDeny,
			Source:     permission.DecisionSourceRoots,
			Reason:     reason + " (no approver available)",
			Capability: cap,
			Summary:    summary,
		}, nil
	}

	choice, err := approver(ctx, permission.Prompt{
		Tool:         req.ToolName,
		Summary:      summary,
		Reason:       reason,
		Capability:   cap,
		OutsideRoots: true, // reuse "restricted options" UI: only Allow Once / Deny
	})
	if err != nil {
		return nil, err
	}
	if choice == permission.ChoiceDeny {
		e.audit(info, e.Mode(), e.PlanMode(), "deny", false, reason)
		return &permission.Decision{
			Kind:       permission.DecisionDeny,
			Source:     permission.DecisionSourcePrompt,
			Reason:     reason,
			Capability: cap,
			Summary:    summary,
			Prompted:   true,
		}, nil
	}
	e.audit(info, e.Mode(), e.PlanMode(), "allow", true, "force-ask one-time approval")
	return &permission.Decision{
		Kind:       permission.DecisionAllowOnce,
		Source:     permission.DecisionSourcePrompt,
		Capability: cap,
		Summary:    summary,
		Prompted:   true,
	}, nil
}

// dangerousPathContext derives the capability and a path-flavoured summary
// for the dangerous-path branches. Defaults to Write capability since the
// branches are write-heavy; read/glob/grep/ls flip to Read.
func dangerousPathContext(req permission.Request) (permission.Capability, string) {
	cap := permission.CapabilityWrite
	switch req.ToolName {
	case "read", "glob", "grep", "ls":
		cap = permission.CapabilityRead
	}
	summary := pathField(req.Args)
	if summary == "" {
		summary = req.ToolName
	}
	return cap, summary
}

func (e *Engine) decidePlanExit(ctx context.Context, req permission.Request) (*permission.Decision, error) {
	e.mu.RLock()
	approver := e.approver
	contentFn := e.planContentProvider
	e.mu.RUnlock()

	mode, planMode := e.Mode(), true
	info := toolInfo{
		tool:       "exit_plan_mode",
		capability: permission.CapabilityInternal,
		summary:    "Approve this plan and exit plan mode?",
		reason:     "Review the plan; approve to leave plan mode and start execution.",
	}

	if approver == nil {
		// No UI wired (headless / tests without an approver). Fall through
		// to allow so the tool runs and exits plan mode unilaterally.
		e.audit(info, mode, planMode, "allow", true, "no approver wired")
		return &permission.Decision{Kind: permission.DecisionAllow, Source: permission.DecisionSourceInternal}, nil
	}

	preview := ""
	if contentFn != nil {
		if c, err := contentFn(); err == nil {
			preview = c
		}
	}
	preview = appendAllowedPromptsPreview(preview, req.Args)
	info.preview = preview

	choice, err := approver(ctx, Prompt{
		Tool:       info.tool,
		Summary:    info.summary,
		Reason:     info.reason,
		Capability: info.capability,
		Preview:    preview,
	})
	if err != nil {
		return nil, err
	}
	if choice == ChoiceDeny {
		reason := "user denied plan; refine and call exit_plan_mode again"
		e.audit(info, mode, planMode, "deny", false, reason)
		return &permission.Decision{
			Kind:    permission.DecisionDeny,
			Source:  permission.DecisionSourcePrompt,
			Reason:  reason,
			Preview: preview,
		}, nil
	}
	e.audit(info, mode, planMode, string(choice), true, "")
	return &permission.Decision{
		Kind:    permission.DecisionAllow,
		Source:  permission.DecisionSourcePrompt,
		Preview: preview,
	}, nil
}

func appendAllowedPromptsPreview(preview string, args json.RawMessage) string {
	if len(args) == 0 {
		return preview
	}
	var a struct {
		AllowedPrompts []struct {
			Tool   string `json:"tool"`
			Prompt string `json:"prompt"`
		} `json:"allowed_prompts"`
	}
	if err := json.Unmarshal(args, &a); err != nil || len(a.AllowedPrompts) == 0 {
		return preview
	}
	var b strings.Builder
	b.WriteString(preview)
	if preview != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("Follow-up actions noted by the model:\n")
	for _, p := range a.AllowedPrompts {
		b.WriteString("- " + p.Tool + ": " + p.Prompt + "\n")
	}
	return b.String()
}

func (e *Engine) SetFilesystemRoots(roots FilesystemRoots) {
	e.tool.SetFilesystemRoots(permission.FilesystemRoots(roots))
}

// FilesystemRoots returns the engine's current read/write roots, letting a
// caller capture them before a temporary change (e.g. entering a worktree) and
// restore them afterwards.
func (e *Engine) FilesystemRoots() FilesystemRoots {
	return FilesystemRoots(e.tool.FilesystemRoots())
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
