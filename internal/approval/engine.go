package approval

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
)

type Mode string

const (
	ModeStrict      Mode = "strict"
	ModeBalanced    Mode = "balanced"
	ModeAcceptEdits Mode = "accept_edits"
	ModeTrust       Mode = "trust"
	ModePlan        Mode = "plan" // 兼容旧调用；新代码应使用 SetPlanMode
)

type Capability string

const (
	CapRead     Capability = "read"
	CapWrite    Capability = "write"
	CapExec     Capability = "exec"
	CapHook     Capability = "hook"
	CapNetwork  Capability = "network"
	CapSubagent Capability = "subagent"
	CapInternal Capability = "internal"
	CapUnknown  Capability = "unknown"
)

type CommandCategory string

const (
	CommandCategoryInfo    CommandCategory = "info"
	CommandCategoryPrompt  CommandCategory = "prompt"
	CommandCategorySession CommandCategory = "session"
	CommandCategoryConfig  CommandCategory = "config"
	CommandCategoryPlan    CommandCategory = "plan"
	CommandCategoryExit    CommandCategory = "exit"
)

type Choice string

const (
	ChoiceAllowOnce    Choice = "allow_once"
	ChoiceAllowSession Choice = "allow_session"
	ChoiceAllowAlways  Choice = "allow_always"
	ChoiceDeny         Choice = "deny"
)

type Prompt struct {
	Tool         string
	Summary      string
	Reason       string
	Capability   Capability
	Preview      string
	OutsideRoots bool // hint to UI: only allow_once and deny are safe choices
}

type ApproverFunc func(ctx context.Context, prompt Prompt) (Choice, error)

type CommandRequest struct {
	Name      string
	Category  CommandCategory
	NeedsIdle bool
	IsRunning bool
	Summary   string
	Preview   string
}

type HookRequest struct {
	Event    string
	Tool     string
	Command  string
	Blocking bool
}

type AuditEntry struct {
	Time       time.Time
	Mode       Mode
	PlanMode   bool
	Tool       string
	Capability Capability
	Summary    string
	Decision   string
	Reason     string
	Allow      bool
}

type FilesystemRoots struct {
	ReadRoots  []string
	WriteRoots []string
}

type Engine struct {
	workspace string
	fsRoots   FilesystemRoots
	rules     *RuleSet

	mu           sync.RWMutex
	mode         Mode
	planActive   bool
	approver     ApproverFunc
	sessionAllow map[string]storedEntry
	skillAllows  []Rule // temporary allow rules from active skill's allowed-tools
	store        *Store
	onAudit      func(AuditEntry)
	toolMeta     map[string]ToolMetadata
}

func NewEngine(cwd string, mode Mode, rules *RuleSet, onAudit func(AuditEntry)) (*Engine, error) {
	store, err := NewStore(config.ApprovalsPath(cwd))
	if err != nil {
		return nil, fmt.Errorf("load approvals: %w", err)
	}
	return &Engine{
		workspace: cwd,
		fsRoots: FilesystemRoots{
			ReadRoots:  []string{cwd},
			WriteRoots: []string{cwd},
		},
		rules:        rules,
		mode:         mode,
		sessionAllow: make(map[string]storedEntry),
		store:        store,
		onAudit:      onAudit,
		toolMeta:     make(map[string]ToolMetadata),
	}, nil
}

func (e *Engine) SetFilesystemRoots(roots FilesystemRoots) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fsRoots = normalizeFilesystemRoots(e.workspace, roots)
}

func (e *Engine) SetMode(mode Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mode == ModePlan {
		e.planActive = true
		return
	}
	e.mode = mode
}

func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

func (e *Engine) SetPlanMode(active bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.planActive = active
}

func (e *Engine) PlanMode() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.planActive
}

// SetSkillAllows replaces the temporary allow rules from a skill's allowed-tools.
// Pass nil or empty to clear. Each entry is parsed as a permission rule
// (e.g. "Read", "Bash(npm test *)"). Invalid entries are silently ignored.
func (e *Engine) SetSkillAllows(rawTools []string) {
	var rules []Rule
	for _, raw := range rawTools {
		r, err := ParseRule(raw)
		if err != nil {
			continue
		}
		rules = append(rules, r)
	}
	e.mu.Lock()
	e.skillAllows = rules
	e.mu.Unlock()
}

func (e *Engine) state() (Mode, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode, e.planActive
}

func (e *Engine) SetApprover(fn ApproverFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.approver = fn
}

func (e *Engine) ReplaceToolMetadata(tools []agentcore.Tool) {
	metadata := make(map[string]ToolMetadata)
	for _, tool := range tools {
		provider, ok := tool.(interface{ ApprovalMetadata() ToolMetadata })
		if !ok {
			continue
		}
		meta := provider.ApprovalMetadata()
		if meta.ToolName == "" {
			meta.ToolName = tool.Name()
		}
		if meta.ToolName == "" {
			continue
		}
		metadata[meta.ToolName] = meta
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.toolMeta = metadata
}

func (e *Engine) ApproveTool(ctx context.Context, req agentcore.ToolApprovalRequest) (*agentcore.ToolApprovalResult, error) {
	info := e.inspectCall(req)
	return e.approveInfo(ctx, info)
}

func (e *Engine) ApproveHook(ctx context.Context, req HookRequest) error {
	info := inspectHook(req)
	result, err := e.approveInfo(ctx, info)
	if err != nil {
		return err
	}
	if result != nil && !result.Approved {
		return errors.New(firstNonEmpty(result.Reason, "hook denied"))
	}
	return nil
}

func (e *Engine) ApproveCommand(_ context.Context, req CommandRequest) error {
	req.Category = normalizeCommandCategory(req.Category)
	info := inspectCommand(req)
	mode, planMode := e.state()

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

func (e *Engine) approveInfo(ctx context.Context, info toolInfo) (*agentcore.ToolApprovalResult, error) {
	mode, planMode := e.state()
	if info.hardDeny != "" {
		e.audit(info, mode, planMode, "deny", false, info.hardDeny)
		return &agentcore.ToolApprovalResult{
			Approved: false,
			Decision: agentcore.ToolApprovalDeny,
			Reason:   info.hardDeny,
		}, nil
	}
	action, reason := e.decide(info, mode, planMode)
	switch action {
	case ruleAllow:
		e.audit(info, mode, planMode, "allow", true, "")
		return nil, nil
	case ruleDeny:
		msg := reason
		if msg == "" {
			msg = "tool denied by current mode"
		}
		e.audit(info, mode, planMode, "deny", false, msg)
		return &agentcore.ToolApprovalResult{
			Approved: false,
			Decision: agentcore.ToolApprovalDeny,
			Reason:   msg,
		}, nil
	case ruleAsk:
		choice, err := e.ask(ctx, info, mode, planMode)
		if err != nil {
			return nil, err
		}
		return e.resolveChoice(info, mode, planMode, choice), nil
	default:
		return nil, nil
	}
}

type ruleAction string

const (
	ruleAllow ruleAction = "allow"
	ruleAsk   ruleAction = "ask"
	ruleDeny  ruleAction = "deny"
)

func (e *Engine) decide(info toolInfo, mode Mode, planMode bool) (ruleAction, string) {
	// Deny rules have highest priority — override everything.
	var ruleResult ruleAction
	var ruleMatched bool
	if e.rules != nil {
		ruleResult, ruleMatched = e.rules.Evaluate(info)
		if ruleMatched && ruleResult == ruleDeny {
			return ruleDeny, "denied by permission rule"
		}
	}

	// Paths outside configured roots always require explicit approval.
	if info.outsideRoots {
		return ruleAsk, info.reason
	}

	// Skill allowed-tools: auto-approve tools granted by the active skill.
	e.mu.RLock()
	skillRules := e.skillAllows
	e.mu.RUnlock()
	for _, r := range skillRules {
		if r.matches(info, false) {
			return ruleAllow, "allowed by skill"
		}
	}

	if e.allowed(info.key) || e.allowedSession(info.capability) {
		return ruleAllow, ""
	}

	// Allow rules take precedence over mode defaults.
	if ruleMatched && ruleResult == ruleAllow {
		return ruleAllow, "allowed by permission rule"
	}

	// Plan mode enforces read-only regardless of mode.
	if planMode {
		switch info.capability {
		case CapRead, CapInternal:
			return ruleAllow, ""
		default:
			return ruleDeny, "plan mode is read-only"
		}
	}

	// Mode-based decisions (strict → balanced → accept-edits → trust).
	switch mode {
	case ModeTrust:
		return ruleAllow, ""
	case ModeAcceptEdits:
		switch info.capability {
		case CapRead, CapInternal, CapWrite:
			return ruleAllow, ""
		default:
			return ruleAsk, "approval required for side effects"
		}
	case ModeStrict:
		switch info.capability {
		case CapRead, CapInternal:
			return ruleAllow, ""
		case CapWrite:
			return ruleAsk, "strict mode requires approval for writes"
		default:
			return ruleDeny, "strict mode denies this capability"
		}
	default: // ModeBalanced
		switch info.capability {
		case CapRead, CapInternal:
			return ruleAllow, ""
		default:
			return ruleAsk, "approval required for side effects"
		}
	}
}

func (e *Engine) ask(ctx context.Context, info toolInfo, mode Mode, planMode bool) (Choice, error) {
	e.mu.RLock()
	fn := e.approver
	e.mu.RUnlock()
	if fn == nil {
		msg := info.reason
		if msg == "" {
			msg = "approval required but no approver is configured"
		}
		e.audit(info, mode, planMode, "deny", false, msg)
		return ChoiceDeny, nil
	}
	return fn(ctx, Prompt{
		Tool:         info.tool,
		Summary:      info.summary,
		Reason:       info.reason,
		Capability:   info.capability,
		Preview:      info.preview,
		OutsideRoots: info.outsideRoots,
	})
}

func (e *Engine) resolveChoice(info toolInfo, mode Mode, planMode bool, choice Choice) *agentcore.ToolApprovalResult {
	// Outside-roots approvals must not be cached at session/always granularity,
	// otherwise a single "allow session" would open the entire filesystem.
	if info.outsideRoots && (choice == ChoiceAllowAlways || choice == ChoiceAllowSession) {
		choice = ChoiceAllowOnce
	}

	switch choice {
	case ChoiceAllowAlways:
		entry := storedEntry{
			Key:        info.key,
			Tool:       info.tool,
			Capability: info.capability,
			Summary:    info.summary,
			AddedAt:    time.Now(),
		}
		e.mu.Lock()
		e.sessionAllow[info.key] = entry
		store := e.store
		e.mu.Unlock()
		if store != nil {
			_ = store.Add(entry)
		}
		e.audit(info, mode, planMode, string(choice), true, "")
		return &agentcore.ToolApprovalResult{
			Approved: true,
			Decision: agentcore.ToolApprovalAllowAlways,
		}
	case ChoiceAllowSession:
		// Store capability-level key so "allow for session" covers all uses
		// of the same capability (e.g. all file writes), matching the behavior
		// of mainstream AI coding agents.
		sKey := "session:" + string(info.capability)
		entry := storedEntry{
			Key:        sKey,
			Tool:       info.tool,
			Capability: info.capability,
			Summary:    info.summary,
			AddedAt:    time.Now(),
		}
		e.mu.Lock()
		e.sessionAllow[sKey] = entry
		e.mu.Unlock()
		e.audit(info, mode, planMode, string(choice), true, "")
		return &agentcore.ToolApprovalResult{
			Approved: true,
			Decision: agentcore.ToolApprovalAllowSession,
		}
	case ChoiceDeny:
		e.audit(info, mode, planMode, string(choice), false, info.reason)
		return &agentcore.ToolApprovalResult{
			Approved: false,
			Decision: agentcore.ToolApprovalDeny,
			Reason:   firstNonEmpty(info.reason, "tool execution denied by user"),
		}
	default:
		e.audit(info, mode, planMode, string(ChoiceAllowOnce), true, "")
		return &agentcore.ToolApprovalResult{
			Approved: true,
			Decision: agentcore.ToolApprovalAllowOnce,
		}
	}
}

// allowedSession checks if the given capability has been approved for the session.
func (e *Engine) allowedSession(cap Capability) bool {
	return e.allowed("session:" + string(cap))
}

func (e *Engine) allowed(key string) bool {
	if key == "" {
		return false
	}
	e.mu.RLock()
	_, ok := e.sessionAllow[key]
	store := e.store
	e.mu.RUnlock()
	if ok {
		return true
	}
	return store != nil && store.Has(key)
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
