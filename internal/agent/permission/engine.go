package permission

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
)

// Config is what an Engine is made from.
type Config struct {
	Cwd   string
	Mode  interact.Mode
	Rules *RuleSet
	Roots FilesystemRoots
	// UI is asked whatever the mode does not allow on its own; without one,
	// that is denied.
	UI      interact.UI
	OnAudit func(AuditEntry)
}

// Engine decides tool calls. Its mode, its rules and roots, which follow
// the settings as reloaded, and the approvals given while it runs change;
// what one conversation allows on top travels with its requests, see
// Middleware.
type Engine struct {
	workspace string
	store     *Store
	ui        interact.UI
	onAudit   func(AuditEntry)

	mu    sync.RWMutex
	mode  interact.Mode
	rules *RuleSet
	roots FilesystemRoots
}

func NewEngine(cfg Config) (*Engine, error) {
	store, err := NewStore(config.ApprovalsPath(cfg.Cwd))
	if err != nil {
		return nil, err
	}
	return &Engine{
		workspace: cfg.Cwd,
		rules:     cfg.Rules,
		roots:     normalizeFilesystemRoots(cfg.Cwd, cfg.Roots),
		store:     store,
		ui:        cfg.UI,
		onAudit:   cfg.OnAudit,
		mode:      cfg.Mode,
	}, nil
}

// Middleware decides the tool calls of one conversation, refusing those it
// does not allow. grants returns what the conversation allows beyond the mode
// at the time of each call, such as the tools of the skills its run invoked;
// meta returns how the engine sees the tools that classify themselves, such
// as MCP tools.
func (e *Engine) Middleware(grants func() []Rule, meta func(tool string) Metadata) agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		decision, err := e.Decide(ctx, Request{
			ToolID:    call.ID,
			ToolName:  call.Name,
			ToolLabel: call.Tool.Label,
			Args:      call.Args,
			Metadata:  meta(call.Name),
			// Paths resolve against the directory the tool runs in, which
			// moves with a worktree entered mid-run.
			Workspace: agentcoretools.CwdFromContext(ctx),
			Grants:    grants(),
		})
		if err != nil {
			return agentcore.Result{}, err
		}
		if !decision.Allowed() {
			return agentcore.ErrorResult(cmp.Or(decision.Reason, "tool execution denied")), nil
		}
		return next(ctx, call)
	}
}

// Configure replaces the rules and the roots, from the next decision on.
func (e *Engine) Configure(rules *RuleSet, roots FilesystemRoots) {
	roots = normalizeFilesystemRoots(e.workspace, roots)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules, e.roots = rules, roots
}

func (e *Engine) Decide(ctx context.Context, req Request) (*Decision, error) {
	// A per-request Workspace (e.g. a worktree the harness entered mid-run)
	// wins over the engine's construction-time workspace, so relative operand
	// paths are normalized, checked, and audited against the directory the
	// tool actually runs in. Empty preserves the original behaviour.
	e.mu.RLock()
	rules, roots := e.rules, e.roots
	e.mu.RUnlock()
	info := inspectRequest(firstNonEmpty(req.Workspace, e.workspace), roots, req)
	if info.hardDeny != "" {
		decision := denyDecision(DecisionSourceRoots, info, info.hardDeny)
		e.audit(info, decision)
		return decision, nil
	}

	rule := rules.evaluate(info)
	if rule == ruleDeny {
		decision := denyDecision(DecisionSourceRule, info, "denied by permission rule")
		e.audit(info, decision)
		return decision, nil
	}

	// A call outside the roots, or one the harness wants confirmed each
	// time, asks whatever the mode and the stored approvals say.
	if info.askEachTime() {
		return e.ask(ctx, info)
	}

	// Harness-declared internal path: silent allow for the requested
	// capability. Deny rules above still apply, so this only bypasses the
	// mode-based ask that would otherwise interrupt every memory or scratch
	// write.
	if info.internalPath {
		decision := allowDecision(DecisionSourceInternal, info, "harness-managed path")
		e.audit(info, decision)
		return decision, nil
	}

	for _, r := range req.Grants {
		if r.matches(info, false) {
			decision := allowDecision(DecisionSourceGrant, info, "allowed by grant")
			e.audit(info, decision)
			return decision, nil
		}
	}

	if e.remembered(info) {
		decision := allowDecision(DecisionSourceStore, info, "allowed by stored approval")
		e.audit(info, decision)
		return decision, nil
	}

	if rule == ruleAllow {
		decision := allowDecision(DecisionSourceRule, info, "allowed by permission rule")
		e.audit(info, decision)
		return decision, nil
	}

	switch e.Mode() {
	case interact.ModeTrust:
		decision := allowDecision(DecisionSourceMode, info, "trust mode allows tool execution")
		e.audit(info, decision)
		return decision, nil
	case interact.ModeAcceptEdits:
		switch info.capability {
		case CapabilityRead, CapabilityInternal, CapabilityWrite:
			decision := allowDecision(DecisionSourceMode, info, "accept-edits mode allows this capability")
			e.audit(info, decision)
			return decision, nil
		default:
			return e.ask(ctx, info)
		}
	case interact.ModeStrict:
		switch info.capability {
		case CapabilityRead, CapabilityInternal:
			decision := allowDecision(DecisionSourceMode, info, "strict mode allows read-only tools")
			e.audit(info, decision)
			return decision, nil
		case CapabilityWrite:
			return e.ask(ctx, info.orReason("strict mode requires approval for writes"))
		default:
			decision := denyDecision(DecisionSourceMode, info, "strict mode denies this capability")
			e.audit(info, decision)
			return decision, nil
		}
	default:
		switch info.capability {
		case CapabilityRead, CapabilityInternal:
			decision := allowDecision(DecisionSourceMode, info, "balanced mode allows read-only tools")
			e.audit(info, decision)
			return decision, nil
		default:
			return e.ask(ctx, info.orReason("approval required for side effects"))
		}
	}
}

func (e *Engine) SetMode(mode interact.Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = mode
}

func (e *Engine) Mode() interact.Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// ask puts the request to the user and audits the outcome.
func (e *Engine) ask(ctx context.Context, info toolInfo) (*Decision, error) {
	if e.ui == nil {
		msg := info.reason
		if msg == "" {
			msg = "approval required but no one can be asked"
		}
		decision := denyDecision(DecisionSourcePrompt, info, msg)
		e.audit(info, decision)
		return decision, nil
	}
	approval := interact.Approval{
		ToolID:       info.toolID,
		Tool:         info.tool,
		Summary:      info.summary,
		Reason:       info.reason,
		OutsideRoots: info.outsideRoots,
		Confirm:      info.askEachTime(),
		Edit:         info.capability == CapabilityWrite && !info.askEachTime(),
	}
	if info.rememberable() {
		approval.Remember = info.remember
	}
	if info.tool == "bash" {
		approval.Warning = destructiveCommandWarning(info.summary)
	}
	verdict, err := e.ui.Approve(ctx, approval)
	if err != nil {
		return nil, err
	}
	decision := e.resolve(info, verdict)
	e.audit(info, decision)
	return decision, nil
}

// resolve turns the user's answer into a decision, remembering what an
// always allows. A denial tells the agent it was the user's, and what they
// said to do instead.
func (e *Engine) resolve(info toolInfo, v interact.Verdict) *Decision {
	d := &Decision{
		Kind:         DecisionAllowOnce,
		Source:       DecisionSourcePrompt,
		Capability:   info.capability,
		Summary:      info.summary,
		OutsideRoots: info.outsideRoots,
		Prompted:     true,
	}
	switch {
	case v.Choice == interact.Deny && v.Feedback != "":
		d.Kind, d.Reason = DecisionDeny, "The user denied this and said what to do instead: "+v.Feedback
	case v.Choice == interact.Deny:
		d.Kind, d.Reason = DecisionDeny, "The user denied this. Do not try to get around it; if it is unclear how to go on, ask them."
	case v.Choice == interact.AllowAlways && info.rememberable():
		for _, key := range info.keys {
			_ = e.store.Add(StoreEntry{Key: key, Tool: info.tool, Capability: info.capability, Summary: info.summary, AddedAt: time.Now()})
		}
		d.Kind = DecisionAllowAlways
	}
	return d
}

// remembered reports whether the user always allowed what the call does.
func (e *Engine) remembered(info toolInfo) bool {
	for _, key := range info.keys {
		if !e.store.Has(key) {
			return false
		}
	}
	return len(info.keys) > 0
}

func (e *Engine) audit(info toolInfo, decision *Decision) {
	if e.onAudit == nil {
		return
	}
	e.onAudit(AuditEntry{
		Time:       time.Now(),
		Mode:       e.Mode(),
		Tool:       info.tool,
		Capability: info.capability,
		Summary:    info.summary,
		Decision:   string(decision.Kind),
		Reason:     decision.Reason,
		Allow:      decision.Allowed(),
	})
}

type toolInfo struct {
	toolID     string
	tool       string
	capability Capability
	summary    string
	// keys are what remembering the call stores, all of which allow it
	// again; remember says what they allow, for the user.
	keys         []string
	remember     string
	reason       string
	hardDeny     string
	outsideRoots bool
	confirm      bool // the classifier asked to confirm each call
	internalPath bool
	workspace    string
	roots        []string
}

// orReason gives the request a reason when its classification gave none.
func (i toolInfo) orReason(reason string) toolInfo {
	if i.reason == "" {
		i.reason = reason
	}
	return i
}

// askEachTime reports whether the call is confirmed every time.
func (i toolInfo) askEachTime() bool { return i.outsideRoots || i.confirm }

// rememberable reports whether always allowing the call can be remembered.
func (i toolInfo) rememberable() bool { return len(i.keys) > 0 && !i.askEachTime() }

// ruleAction is what the matching rule says of a request; "" when none
// matches.
type ruleAction string

const (
	ruleAllow ruleAction = "allow"
	ruleDeny  ruleAction = "deny"
)

func denyDecision(source DecisionSource, info toolInfo, reason string) *Decision {
	return &Decision{
		Kind:         DecisionDeny,
		Source:       source,
		Reason:       reason,
		Capability:   info.capability,
		Summary:      info.summary,
		OutsideRoots: info.outsideRoots,
	}
}

func allowDecision(source DecisionSource, info toolInfo, reason string) *Decision {
	return &Decision{
		Kind:         DecisionAllow,
		Source:       source,
		Reason:       reason,
		Capability:   info.capability,
		Summary:      info.summary,
		OutsideRoots: info.outsideRoots,
	}
}

func inspectRequest(workspace string, roots FilesystemRoots, req Request) toolInfo {
	c := classify(workspace, req)
	info := toolInfo{
		toolID:     req.ToolID,
		tool:       req.ToolName,
		capability: c.capability,
		summary:    strings.TrimSpace(req.Summary),
		reason:     strings.TrimSpace(req.Reason),
		workspace:  workspace,
	}
	if info.capability == "" {
		info.capability = CapabilityUnknown
	}
	if info.summary == "" {
		info.summary = req.ToolName
	}

	switch info.capability {
	case CapabilityRead:
		info.roots = roots.ReadRoots
		if c.path != "" {
			path, deny := checkedPath(workspace, roots.ReadRoots, c.path, "readable")
			if path != "" {
				info.summary = path
			}
			if deny != "" {
				// Harness-declared internal paths bypass the user-roots check.
				// InternalWritable implies readability — populating only the
				// writable list is the common case for fully-managed dirs.
				if pathInRoots(path, roots.InternalReadable) || pathInRoots(path, roots.InternalWritable) {
					info.internalPath = true
				} else {
					info.outsideRoots = true
					info.reason = deny
				}
			}
		}
	case CapabilityWrite:
		info.roots = roots.WriteRoots
		path, deny := checkedPath(workspace, roots.WriteRoots, c.path, "writable")
		info.summary = firstNonEmpty(path, info.summary)
		if info.reason == "" {
			info.reason = "file modification requires approval"
		}
		if pathInRoots(path, roots.Protected) {
			c.confirm = "a directory deciding what codebot runs (" + path + ") requires per-invocation approval"
		}
		if deny != "" {
			switch {
			case pathInRoots(path, roots.InternalWritable):
				info.internalPath = true
			case pathInRoots(path, roots.InternalReadable):
				info.hardDeny = fmt.Sprintf("path in read-only internal root, not writable: %s", path)
			default:
				_, notInReadRoots := checkedPath(workspace, roots.ReadRoots, c.path, "readable")
				if notInReadRoots == "" {
					info.hardDeny = fmt.Sprintf("path in read-only root, not writable: %s", path)
				} else {
					info.outsideRoots = true
					info.reason = deny
				}
			}
		}
	case CapabilityExec:
		command := strings.TrimSpace(c.command)
		info.summary = firstNonEmpty(command, info.summary)
		info.keys = commandKeys(command)
		info.remember = commands(info.keys)
		if info.reason == "" {
			info.reason = "shell execution requires approval"
		}
		if wd := strings.TrimSpace(c.workdir); wd != "" {
			_, deny := checkedPath(workspace, roots.WriteRoots, wd, "writable")
			if deny != "" {
				info.outsideRoots = true
				info.reason = fmt.Sprintf("workdir outside writable roots: %s", wd)
			}
		}
	case CapabilityNetwork:
		target := strings.TrimSpace(c.url)
		info.summary = firstNonEmpty(target, info.summary)
		if target != "" {
			info.keys, info.remember = []string{"network:" + hostOf(target)}, "requests to "+hostOf(target)
		} else {
			info.keys, info.remember = []string{"network:" + req.ToolName}, "`"+req.ToolName+"`"
		}
		if info.reason == "" {
			info.reason = "network access requires approval"
		}
	case CapabilityInternal:
	default:
		info.capability = CapabilityUnknown
		info.keys, info.remember = []string{"tool:" + req.ToolName}, "`"+req.ToolName+"`"
		if info.reason == "" {
			info.reason = "unclassified tool requires approval"
		}
	}

	meta := req.Metadata
	if meta.Capability != "" {
		info.capability = meta.Capability
	}
	if meta.SummaryHint != "" && strings.TrimSpace(info.summary) == req.ToolName {
		info.summary = meta.SummaryHint
	}
	if meta.Reason != "" {
		info.reason = meta.Reason
	}
	if meta.Key != "" || meta.KeyPrefix != "" {
		info.keys = []string{cmp.Or(meta.Key, meta.KeyPrefix+":"+req.ToolName)}
		info.remember = "`" + firstNonEmpty(req.ToolLabel, meta.SummaryHint, req.ToolName) + "`"
	}
	if info.remember != "" {
		info.remember += " in this project"
	}
	if c.confirm != "" {
		info.confirm = true
		info.reason = c.confirm
	}
	return info
}

// checkedPath resolves raw against workspace, then verifies it falls within
// at least one of roots. Returns the absolute path plus a deny message when
// the path lies outside; an empty raw input returns ("", "").
func checkedPath(workspace string, roots []string, raw, rootLabel string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	var path string
	switch {
	case filepath.IsAbs(raw):
		path = filepath.Clean(raw)
	case workspace == "":
		path = filepath.Clean(raw)
	default:
		path = filepath.Clean(filepath.Join(workspace, raw))
	}
	if len(roots) == 0 {
		if workspace == "" {
			return path, ""
		}
		roots = []string{workspace}
	}
	if pathInRoots(path, roots) {
		return path, ""
	}
	return path, fmt.Sprintf("path outside %s roots denied: %s", rootLabel, path)
}

// pathInRoots reports whether path resolves under any of the given roots.
// Both sides are passed through resolveSymlinks so a symlink inside or below
// a root resolves consistently with the canonical comparison checkedPath
// performs. A miss returns false without producing a deny message — callers
// such as inspectRequest's InternalReadable/InternalWritable check fall
// through to the next decision step on miss.
func pathInRoots(path string, roots []string) bool {
	if path == "" || len(roots) == 0 {
		return false
	}
	target := resolveSymlinks(path)
	for _, root := range roots {
		base := resolveSymlinks(filepath.Clean(root))
		if isSubPath(base, target) {
			return true
		}
	}
	return false
}

func resolveSymlinks(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	current := cleaned
	tail := ""
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			if tail == "" {
				return resolved
			}
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(current)
		if parent == current {
			if tail == "" {
				return cleaned
			}
			return filepath.Join(current, tail)
		}
		base := filepath.Base(current)
		if tail == "" {
			tail = base
		} else {
			tail = filepath.Join(base, tail)
		}
		current = parent
	}
}

func isSubPath(base, target string) bool {
	if base == "" {
		return true
	}
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func normalizeFilesystemRoots(workspace string, roots FilesystemRoots) FilesystemRoots {
	readRoots := dedup(roots.ReadRoots)
	writeRoots := dedup(roots.WriteRoots)
	if len(readRoots) == 0 && workspace != "" {
		readRoots = []string{filepath.Clean(workspace)}
	}
	if len(writeRoots) == 0 && workspace != "" {
		writeRoots = []string{filepath.Clean(workspace)}
	}
	return FilesystemRoots{
		ReadRoots:        readRoots,
		WriteRoots:       writeRoots,
		InternalReadable: dedup(roots.InternalReadable),
		InternalWritable: dedup(roots.InternalWritable),
		Protected:        dedup(roots.Protected),
	}
}

func dedup(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

func hostOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "unknown"
	}
	if parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	return "unknown"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// commands names the commands keys allow: "`go test` and `go vet`
// commands".
func commands(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	names := make([]string, len(keys))
	for i, key := range keys {
		names[i] = "`" + strings.TrimPrefix(key, "exec:") + "`"
	}
	if n := len(names); n > 1 {
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1] + " commands"
	}
	return names[0] + " commands"
}
