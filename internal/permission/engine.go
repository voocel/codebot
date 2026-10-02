package permission

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Engine decides tool requests. Everything but the mode and the approvals
// given while it runs is fixed when it is made.
type Engine struct {
	workspace  string
	rules      *RuleSet
	store      *Store
	onAudit    func(AuditEntry)
	classifier Classifier
	approver   Approver
	fsRoots    FilesystemRoots

	mu           sync.RWMutex
	mode         Mode
	sessionAllow map[string]StoreEntry
}

func NewEngine(cfg EngineConfig) *Engine {
	return &Engine{
		workspace:    cfg.Workspace,
		rules:        cfg.Rules,
		store:        cfg.Store,
		onAudit:      cfg.OnAudit,
		classifier:   cfg.Classifier,
		approver:     cfg.Approver,
		fsRoots:      normalizeFilesystemRoots(cfg.Workspace, cfg.Roots),
		mode:         cfg.Mode,
		sessionAllow: make(map[string]StoreEntry),
	}
}

func (e *Engine) Decide(ctx context.Context, req Request) (*Decision, error) {
	// A per-request Workspace (e.g. a worktree the harness entered mid-run)
	// wins over the engine's construction-time workspace, so relative operand
	// paths are normalized, checked, and audited against the directory the
	// tool actually runs in. Empty preserves the original behaviour.
	info := inspectRequest(firstNonEmpty(req.Workspace, e.workspace), e.fsRoots, e.classifier, req)
	if info.hardDeny != "" {
		decision := denyDecision(DecisionSourceRoots, info, info.hardDeny)
		e.audit(info, decision)
		return decision, nil
	}

	rule := e.rules.evaluate(info)
	if rule == ruleDeny {
		decision := denyDecision(DecisionSourceRule, info, "denied by permission rule")
		e.audit(info, decision)
		return decision, nil
	}

	// A call outside the roots, or one the harness wants confirmed each
	// time, asks whatever the mode and the stored approvals say.
	if info.onceOnly() {
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

	if e.allowed(info.key) || e.allowedSession(info.capability) {
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
	case ModeTrust:
		decision := allowDecision(DecisionSourceMode, info, "trust mode allows tool execution")
		e.audit(info, decision)
		return decision, nil
	case ModeAcceptEdits:
		switch info.capability {
		case CapabilityRead, CapabilityInternal, CapabilityWrite:
			decision := allowDecision(DecisionSourceMode, info, "accept-edits mode allows this capability")
			e.audit(info, decision)
			return decision, nil
		default:
			return e.ask(ctx, info)
		}
	case ModeStrict:
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

func (e *Engine) SetMode(mode Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = mode
}

func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// ask puts the request to the approver and audits the outcome.
func (e *Engine) ask(ctx context.Context, info toolInfo) (*Decision, error) {
	if e.approver == nil {
		msg := info.reason
		if msg == "" {
			msg = "approval required but no approver is configured"
		}
		decision := denyDecision(DecisionSourcePrompt, info, msg)
		e.audit(info, decision)
		return decision, nil
	}
	choice, err := e.approver(ctx, Prompt{
		ToolID:       info.toolID,
		Tool:         info.tool,
		Summary:      info.summary,
		Reason:       info.reason,
		Capability:   info.capability,
		OutsideRoots: info.outsideRoots,
		OnceOnly:     info.onceOnly(),
	})
	if err != nil {
		return nil, err
	}
	decision := e.resolveChoice(info, choice)
	e.audit(info, decision)
	return decision, nil
}

func (e *Engine) resolveChoice(info toolInfo, choice Choice) *Decision {
	if info.onceOnly() && (choice == ChoiceAllowAlways || choice == ChoiceAllowSession) {
		choice = ChoiceAllowOnce
	}
	switch choice {
	case ChoiceAllowAlways:
		entry := StoreEntry{
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
		return &Decision{
			Kind:         DecisionAllowAlways,
			Source:       DecisionSourcePrompt,
			Capability:   info.capability,
			Summary:      info.summary,
			Key:          info.key,
			OutsideRoots: info.outsideRoots,
			Prompted:     true,
		}
	case ChoiceAllowSession:
		sKey := "session:" + string(info.capability)
		entry := StoreEntry{
			Key:        sKey,
			Tool:       info.tool,
			Capability: info.capability,
			Summary:    info.summary,
			AddedAt:    time.Now(),
		}
		e.mu.Lock()
		e.sessionAllow[sKey] = entry
		e.mu.Unlock()
		return &Decision{
			Kind:         DecisionAllowSession,
			Source:       DecisionSourcePrompt,
			Capability:   info.capability,
			Summary:      info.summary,
			Key:          sKey,
			OutsideRoots: info.outsideRoots,
			Prompted:     true,
		}
	case ChoiceDeny:
		return &Decision{
			Kind:         DecisionDeny,
			Source:       DecisionSourcePrompt,
			Reason:       firstNonEmpty(info.reason, "tool execution denied by user"),
			Capability:   info.capability,
			Summary:      info.summary,
			Key:          info.key,
			OutsideRoots: info.outsideRoots,
			Prompted:     true,
		}
	default:
		return &Decision{
			Kind:         DecisionAllowOnce,
			Source:       DecisionSourcePrompt,
			Capability:   info.capability,
			Summary:      info.summary,
			Key:          info.key,
			OutsideRoots: info.outsideRoots,
			Prompted:     true,
		}
	}
}

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
	toolID       string
	tool         string
	capability   Capability
	summary      string
	key          string
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

// onceOnly reports whether the request needs asking every time.
func (i toolInfo) onceOnly() bool { return i.outsideRoots || i.confirm }

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
		Key:          info.key,
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
		Key:          info.key,
		OutsideRoots: info.outsideRoots,
	}
}

func inspectRequest(workspace string, roots FilesystemRoots, classifier Classifier, req Request) toolInfo {
	var c Classification
	if classifier != nil {
		c = classifier(req)
	}
	info := toolInfo{
		toolID:     req.ToolID,
		tool:       req.ToolName,
		capability: c.Capability,
		summary:    strings.TrimSpace(req.Summary),
		reason:     strings.TrimSpace(req.Reason),
		workspace:  workspace,
	}
	if info.capability == "" {
		info.capability = CapabilityUnknown
	}
	if info.summary == "" {
		info.summary = strings.TrimSpace(c.Summary)
	}
	if info.summary == "" {
		info.summary = req.ToolName
	}
	if info.reason == "" {
		info.reason = strings.TrimSpace(c.Reason)
	}

	switch info.capability {
	case CapabilityRead:
		info.key = "read"
		info.roots = roots.ReadRoots
		if c.Path != "" {
			path, deny := checkedPath(workspace, roots.ReadRoots, c.Path, "readable")
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
		path, deny := checkedPath(workspace, roots.WriteRoots, c.Path, "writable")
		info.summary = firstNonEmpty(path, info.summary)
		info.key = "write:" + path
		if info.reason == "" {
			info.reason = "file modification requires approval"
		}
		if deny != "" {
			switch {
			case pathInRoots(path, roots.InternalWritable):
				info.internalPath = true
			case pathInRoots(path, roots.InternalReadable):
				info.hardDeny = fmt.Sprintf("path in read-only internal root, not writable: %s", path)
			default:
				_, notInReadRoots := checkedPath(workspace, roots.ReadRoots, c.Path, "readable")
				if notInReadRoots == "" {
					info.hardDeny = fmt.Sprintf("path in read-only root, not writable: %s", path)
				} else {
					info.outsideRoots = true
					info.reason = deny
				}
			}
		}
	case CapabilityExec:
		command := strings.TrimSpace(c.Command)
		info.summary = firstNonEmpty(command, info.summary)
		info.key = "exec:" + shortHash(command)
		if info.reason == "" {
			info.reason = "shell execution requires approval"
		}
		if wd := strings.TrimSpace(c.Workdir); wd != "" {
			_, deny := checkedPath(workspace, roots.WriteRoots, wd, "writable")
			if deny != "" {
				info.outsideRoots = true
				info.reason = fmt.Sprintf("workdir outside writable roots: %s", wd)
			}
		}
	case CapabilityNetwork:
		target := strings.TrimSpace(c.URL)
		info.summary = firstNonEmpty(target, info.summary)
		if target != "" {
			info.key = "network:" + hostOf(target)
		} else {
			info.key = "network:" + req.ToolName
		}
		if info.reason == "" {
			info.reason = "network access requires approval"
		}
	case CapabilityInternal:
		info.key = "internal:" + req.ToolName
	default:
		info.capability = CapabilityUnknown
		info.key = "tool:" + req.ToolName
		if info.reason == "" {
			info.reason = "unclassified tool requires approval"
		}
	}

	if c.Key != "" {
		info.key = c.Key
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
	if meta.Key != "" {
		info.key = meta.Key
	} else if meta.KeyPrefix != "" {
		info.key = meta.KeyPrefix + ":" + req.ToolName
	}
	if c.Confirm != "" {
		info.confirm = true
		info.reason = c.Confirm
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
