package permission

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/lib/printable"
)

type Config struct {
	Cwd     string
	Rules   *RuleSet
	Roots   FilesystemRoots
	OnAudit func(AuditEntry)
}

// Engine holds the project's state: rules, roots and stored approvals. What
// one conversation brings, its mode, who to ask and what it allows on top,
// comes with each request; see Middleware.
type Engine struct {
	workspace string
	store     *Store
	onAudit   func(AuditEntry)

	mu    sync.RWMutex
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
		onAudit:   cfg.OnAudit,
	}, nil
}

// Scope is what one conversation brings to its tool calls; each is read
// for each call.
type Scope struct {
	Mode func() interact.Mode
	// UI is asked what the mode and approvals leave open; nil denies it.
	UI interact.UI
	// Roots are read and written like the workspace, such as the other
	// checkout of the project the conversation runs in.
	Roots []string
	// Grants returns extra allowances, such as the tools of skills invoked
	// in the run.
	Grants func() []Rule
	// Meta returns the Metadata of self-classifying tools such as MCP tools.
	Meta func(tool string) Metadata
	// Sandboxed means bash runs commands in a sandbox that keeps them in the
	// workspace and off the network, unless a call sets unsandboxed. Those
	// it keeps need no approval, except in strict mode.
	Sandboxed bool
}

// Middleware gates one conversation's tool calls.
func (e *Engine) Middleware(s Scope) agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		decision, err := e.Decide(ctx, Request{
			ToolID:    call.ID,
			ToolName:  call.Name,
			ToolLabel: call.Tool.Label,
			Args:      call.Args,
			Metadata:  s.Meta(call.Name),
			// The tool's cwd moves when a worktree is entered mid-run.
			Workspace: agentcoretools.CwdFromContext(ctx),
			Mode:      s.Mode(),
			UI:        s.UI,
			Roots:     s.Roots,
			Grants:    s.Grants(),
			Sandboxed: s.Sandboxed,
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

// Roots returns the roots in effect.
func (e *Engine) Roots() FilesystemRoots {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.roots
}

func (e *Engine) Configure(rules *RuleSet, roots FilesystemRoots) {
	roots = normalizeFilesystemRoots(e.workspace, roots)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules, e.roots = rules, roots
}

func (e *Engine) Decide(ctx context.Context, req Request) (*Decision, error) {
	e.mu.RLock()
	rules, roots := e.rules, e.roots
	e.mu.RUnlock()
	if len(req.Roots) > 0 {
		roots.ReadRoots = slices.Concat(roots.ReadRoots, req.Roots)
		roots.WriteRoots = slices.Concat(roots.WriteRoots, req.Roots)
	}
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

	// Outside-roots and confirm-each-time calls ask regardless of the mode
	// and stored approvals.
	if info.askEachTime() {
		return e.ask(ctx, req.UI, info)
	}

	// Skips the mode prompt that would otherwise interrupt every memory
	// write. Deny rules above still apply.
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

	if info.sandboxed && req.Mode != interact.ModeStrict {
		decision := allowDecision(DecisionSourceSandbox, info, "runs in the sandbox")
		e.audit(info, decision)
		return decision, nil
	}

	switch req.Mode {
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
			return e.ask(ctx, req.UI, info)
		}
	case interact.ModeStrict:
		switch info.capability {
		case CapabilityRead, CapabilityInternal:
			decision := allowDecision(DecisionSourceMode, info, "strict mode allows read-only tools")
			e.audit(info, decision)
			return decision, nil
		case CapabilityWrite:
			return e.ask(ctx, req.UI, info.orReason("strict mode requires approval for writes"))
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
			return e.ask(ctx, req.UI, info.orReason("approval required for side effects"))
		}
	}
}

func (e *Engine) ask(ctx context.Context, ui interact.UI, info toolInfo) (*Decision, error) {
	if ui == nil {
		msg := info.reason
		if msg == "" {
			msg = "approval required but no one can be asked"
		}
		decision := denyDecision(DecisionSourcePrompt, info, msg)
		e.audit(info, decision)
		return decision, nil
	}
	approval := interact.Approval{
		ToolID: info.toolID,
		Tool:   info.tool,
		// Both come from the model, which could hide a part of the call
		// from the user with characters the terminal acts on.
		Summary:      printable.Lines(info.summary),
		Intent:       printable.Lines(info.intent),
		Dir:          info.dir,
		Reason:       info.reason,
		OutsideRoots: info.outsideRoots,
		Confirm:      info.askEachTime(),
		Edit:         info.capability == CapabilityWrite && !info.askEachTime(),
		Unsandboxed:  info.unsandboxed,
	}
	if info.rememberable() {
		approval.Remember = info.remember
	}
	if info.tool == "bash" {
		approval.Warning = destructiveCommandWarning(info.summary)
	}
	verdict, err := ui.Approve(ctx, approval)
	if err != nil {
		return nil, err
	}
	decision := e.resolve(info, verdict)
	e.audit(info, decision)
	return decision, nil
}

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

// Remembered is a kind of call the user asked not to be asked about again.
type Remembered struct {
	Key     string
	Command bool   // a shell command, rather than a tool
	Name    string // the command, as "go test", or the tool
	Example string // the call it was given on
	Added   time.Time
}

// Remembered returns the remembered approvals, newest first.
func (e *Engine) Remembered() []Remembered {
	var out []Remembered
	for _, s := range e.store.List() {
		kind, name, _ := strings.Cut(s.Key, ":")
		out = append(out, Remembered{Key: s.Key, Command: kind == "exec", Name: name, Example: s.Summary, Added: s.AddedAt})
	}
	return out
}

// Forget drops a remembered approval, so its calls are asked about again.
func (e *Engine) Forget(key string) error { return e.store.Remove(key) }

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
		Mode:       info.mode,
		Tool:       info.tool,
		Capability: info.capability,
		Summary:    info.summary,
		Decision:   string(decision.Kind),
		Reason:     decision.Reason,
		Allow:      decision.Allowed(),
	})
}

type toolInfo struct {
	mode       interact.Mode
	toolID     string
	tool       string
	capability Capability
	summary    string
	intent     string
	dir        string
	// keys are stored on Allow Always; the call is allowed again only when
	// all of them are stored. remember describes them to the user.
	keys         []string
	remember     string
	reason       string
	hardDeny     string
	outsideRoots bool
	confirm      bool
	internalPath bool
	sandboxed    bool // the command runs in a sandbox that contains it
	unsandboxed  bool // the command asks to run outside the sandbox
	workspace    string
	roots        []string
}

func (i toolInfo) orReason(reason string) toolInfo {
	if i.reason == "" {
		i.reason = reason
	}
	return i
}

func (i toolInfo) askEachTime() bool { return i.outsideRoots || i.confirm }

func (i toolInfo) rememberable() bool { return len(i.keys) > 0 && !i.askEachTime() }

// ruleAction is "" when no rule matches.
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
		mode:       req.Mode,
		toolID:     req.ToolID,
		tool:       req.ToolName,
		capability: c.capability,
		summary:    strings.TrimSpace(req.Summary),
		intent:     strings.TrimSpace(c.intent),
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
		info.summary = firstNonEmpty(strings.TrimSpace(c.url), info.summary)
		if c.path != "" {
			path, deny := checkedPath(workspace, roots.ReadRoots, c.path, "readable")
			if path != "" {
				info.summary = path
			}
			if deny != "" {
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
		info.keys = c.keys
		info.remember = commands(info.keys)
		if info.reason == "" {
			info.reason = "shell execution requires approval"
		}
		info.sandboxed, info.unsandboxed = req.Sandboxed && !c.unsandboxed, c.unsandboxed
		if wd := strings.TrimSpace(c.workdir); wd != "" {
			dir, deny := checkedPath(workspace, roots.WriteRoots, wd, "writable")
			if dir != filepath.Clean(workspace) {
				info.dir = dir
			}
			if deny != "" {
				info.outsideRoots = true
				info.reason = fmt.Sprintf("workdir outside writable roots: %s", wd)
			}
		}
	case CapabilityNetwork:
		info.summary = firstNonEmpty(strings.TrimSpace(c.url), info.summary)
		if host := hostOf(c.url); host != "" {
			info.keys, info.remember = []string{"fetch:" + host}, "fetches from `"+host+"`"
		}
		if info.reason == "" {
			info.reason = "a request to a host can carry data out"
		}
	case CapabilityInternal:
	default:
		info.capability = CapabilityUnknown
		info.keys, info.remember = []string{"tool:" + req.ToolName}, "`"+req.ToolName+"`"
		if info.reason == "" {
			info.reason = "unclassified tool requires approval"
		}
	}
	// What a command reads is held to the readable roots, as the read
	// tool's reads are.
	dir := cmp.Or(absPath(workspace, c.workdir), workspace)
	for _, p := range c.reads {
		path, deny := checkedPath(dir, roots.ReadRoots, p, "readable")
		if deny != "" && !pathInRoots(path, roots.InternalReadable) && !pathInRoots(path, roots.InternalWritable) {
			info.outsideRoots = true
			info.reason = "command reads outside the readable roots: " + path
			break
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

// checkedPath returns the absolute path, and a deny message if it lies
// outside roots.
func checkedPath(workspace string, roots []string, raw, rootLabel string) (string, string) {
	path := absPath(workspace, raw)
	if path == "" {
		return "", ""
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

// absPath returns raw cleaned, taken from workspace if relative; "" for
// none.
func absPath(workspace, raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return ""
	case filepath.IsAbs(raw) || workspace == "":
		return filepath.Clean(raw)
	}
	return filepath.Join(workspace, raw)
}

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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// commands describes keys to the user: "`go test` and `go vet` commands".
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
