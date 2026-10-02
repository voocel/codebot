package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testClassifier maps a fixed set of conventional tool names to capabilities
// for the test suite. Production callers register their own classifier via
// EngineConfig.Classifier; the engine itself has no built-in tool name knowledge.
func testClassifier(req Request) Classification {
	args := map[string]any{}
	if len(req.Args) > 0 {
		_ = json.Unmarshal(req.Args, &args)
	}
	str := func(key string) string {
		v, _ := args[key].(string)
		return v
	}
	switch req.ToolName {
	case "read", "glob", "grep", "ls":
		return Classification{Capability: CapabilityRead, Path: str("path")}
	case "write", "edit":
		return Classification{Capability: CapabilityWrite, Path: str("path")}
	case "bash":
		return Classification{
			Capability: CapabilityExec,
			Command:    str("command"),
			Workdir:    str("workdir"),
		}
	case "web_fetch":
		return Classification{Capability: CapabilityNetwork, URL: str("url")}
	case "web_search":
		return Classification{Capability: CapabilityNetwork, Key: "network:search"}
	}
	return Classification{}
}

// newTestEngine makes an engine over workspace with the test classifier and
// a throwaway store; cfg supplies the rest.
func newTestEngine(t *testing.T, workspace string, cfg EngineConfig) *Engine {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "approvals.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg.Workspace = workspace
	cfg.Store = store
	cfg.Classifier = testClassifier
	return NewEngine(cfg)
}

func mustNotAsk(t *testing.T) Approver {
	return func(context.Context, Prompt) (Choice, error) {
		t.Errorf("the user was asked")
		return ChoiceDeny, nil
	}
}

func toolReq(name string, args map[string]any) Request {
	raw, _ := json.Marshal(args)
	return Request{
		ToolName: name,
		Args:     raw,
	}
}

func TestBalancedReadAllowed(t *testing.T) {
	engine := newTestEngine(t, t.TempDir(), EngineConfig{})

	decision, err := engine.Decide(context.Background(), toolReq("read", map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Kind != DecisionAllow || !decision.Allowed() {
		t.Fatalf("expected auto allow, got %#v", decision)
	}
}

func TestBalancedWriteDeniedWithoutApprover(t *testing.T) {
	engine := newTestEngine(t, t.TempDir(), EngineConfig{})

	decision, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Kind != DecisionDeny || decision.Allowed() {
		t.Fatalf("expected deny without approver, got %#v", decision)
	}
}

// TestRequestWorkspaceOverridesPathBase: a per-request Workspace changes the
// base relative operand paths resolve (and audit) against, so a moved cwd (e.g.
// into a worktree) checks the right directory.
func TestRequestWorkspaceOverridesPathBase(t *testing.T) {
	main := t.TempDir()
	wt := t.TempDir()
	var summaries []string
	engine := NewEngine(EngineConfig{
		Workspace:  main,
		Mode:       ModeTrust, // auto-allow; we only assert the normalized path
		Classifier: testClassifier,
		Roots:      FilesystemRoots{ReadRoots: []string{main, wt}, WriteRoots: []string{main, wt}},
		OnAudit:    func(e AuditEntry) { summaries = append(summaries, e.Summary) },
	})

	// No per-request workspace → relative path resolves against the engine
	// workspace (main).
	if _, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": "a.txt"})); err != nil {
		t.Fatalf("Decide default: %v", err)
	}
	// Per-request workspace → the same relative path resolves against wt.
	req := toolReq("write", map[string]any{"path": "a.txt"})
	req.Workspace = wt
	if _, err := engine.Decide(context.Background(), req); err != nil {
		t.Fatalf("Decide override: %v", err)
	}

	if len(summaries) != 2 {
		t.Fatalf("want 2 audit entries, got %d", len(summaries))
	}
	if want := filepath.Join(main, "a.txt"); summaries[0] != want {
		t.Errorf("default summary = %q, want %q", summaries[0], want)
	}
	if want := filepath.Join(wt, "a.txt"); summaries[1] != want {
		t.Errorf("per-request workspace summary = %q, want %q", summaries[1], want)
	}
}

func TestOutsideRootsAllowSessionDegradesToAllowOnce(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	var prompt Prompt
	engine := newTestEngine(t, workspace, EngineConfig{Approver: func(_ context.Context, p Prompt) (Choice, error) {
		prompt = p
		return ChoiceAllowSession, nil
	}})

	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "read",
		Args:     mustJSON(t, map[string]any{"path": outside}),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Kind != DecisionAllowOnce || !decision.Prompted || !decision.OutsideRoots {
		t.Fatalf("expected prompted allow-once for outside roots, got %#v", decision)
	}
	if !prompt.OutsideRoots || !prompt.OnceOnly {
		t.Fatalf("prompt = %#v, want it outside the roots and once only", prompt)
	}
}

func TestPromptCarriesTheToolCallID(t *testing.T) {
	workspace := t.TempDir()
	var got string
	engine := newTestEngine(t, workspace, EngineConfig{Approver: func(_ context.Context, p Prompt) (Choice, error) {
		got = p.ToolID
		return ChoiceDeny, nil
	}})
	req := toolReq("write", map[string]any{"path": "a.txt"})
	req.ToolID = "call_1"
	if _, err := engine.Decide(context.Background(), req); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got != "call_1" {
		t.Fatalf("prompt ToolID = %q, want call_1", got)
	}
}

func TestWriteViaSymlinkEscapeDenied(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	engine := newTestEngine(t, workspace, EngineConfig{})
	decision, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": "link/escape.txt"}))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected denial for symlink escape, got %#v", decision)
	}
}

func TestMetadataOverrideForCustomTool(t *testing.T) {
	engine := newTestEngine(t, t.TempDir(), EngineConfig{})

	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "custom_lookup",
		Metadata: Metadata{
			Capability:  CapabilityRead,
			SummaryHint: "custom lookup",
			KeyPrefix:   "custom",
		},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Kind != DecisionAllow || decision.Key != "custom:custom_lookup" {
		t.Fatalf("expected metadata-driven allow, got %#v", decision)
	}
}

func TestInternalReadablePathSilentlyAllowed(t *testing.T) {
	workspace := t.TempDir()
	memDir := t.TempDir()
	engine := newTestEngine(t, workspace, EngineConfig{
		Roots:    FilesystemRoots{InternalReadable: []string{memDir}},
		Approver: mustNotAsk(t),
	})

	target := filepath.Join(memDir, "MEMORY.md")
	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "read",
		Args:     mustJSON(t, map[string]any{"path": target}),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || !decision.Allowed() {
		t.Fatalf("expected silent allow, got %#v", decision)
	}
	if decision.Source != DecisionSourceInternal {
		t.Fatalf("expected DecisionSourceInternal, got %q", decision.Source)
	}
	if decision.Prompted {
		t.Fatalf("expected no prompt for internal path, got %#v", decision)
	}
	if decision.OutsideRoots {
		t.Fatalf("internal path must not be marked outside roots, got %#v", decision)
	}
}

func TestInternalWritablePathSilentlyAllowedInBalancedMode(t *testing.T) {
	workspace := t.TempDir()
	memDir := t.TempDir()
	engine := newTestEngine(t, workspace, EngineConfig{
		Roots:    FilesystemRoots{InternalWritable: []string{memDir}},
		Approver: mustNotAsk(t),
	})

	target := filepath.Join(memDir, "MEMORY.md")
	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "write",
		Args:     mustJSON(t, map[string]any{"path": target}),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || !decision.Allowed() || decision.Source != DecisionSourceInternal {
		t.Fatalf("expected silent allow via internal path, got %#v", decision)
	}
}

func TestInternalWritableImpliesReadable(t *testing.T) {
	workspace := t.TempDir()
	memDir := t.TempDir()
	engine := newTestEngine(t, workspace, EngineConfig{
		Roots:    FilesystemRoots{InternalWritable: []string{memDir}},
		Approver: mustNotAsk(t),
	})

	target := filepath.Join(memDir, "topic.md")
	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "read",
		Args:     mustJSON(t, map[string]any{"path": target}),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || !decision.Allowed() || decision.Source != DecisionSourceInternal {
		t.Fatalf("expected internal-path read allow, got %#v", decision)
	}
}

func TestInternalReadOnlyHardDeniesWrite(t *testing.T) {
	workspace := t.TempDir()
	memDir := t.TempDir()
	engine := newTestEngine(t, workspace, EngineConfig{
		Roots: FilesystemRoots{InternalReadable: []string{memDir}},
	})

	target := filepath.Join(memDir, "MEMORY.md")
	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "write",
		Args:     mustJSON(t, map[string]any{"path": target}),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected hard deny for write to read-only internal path, got %#v", decision)
	}
	if decision.Source != DecisionSourceRoots {
		t.Fatalf("expected hard deny via roots, got source %q", decision.Source)
	}
}

func TestInternalPathRespectsDenyRule(t *testing.T) {
	workspace := t.TempDir()
	memDir := t.TempDir()
	target := filepath.Join(memDir, "secret.md")
	rules, err := ParseRuleSet(nil, []string{"Read(" + target + ")"})
	if err != nil {
		t.Fatalf("ParseRuleSet: %v", err)
	}
	engine := newTestEngine(t, workspace, EngineConfig{
		Rules: rules,
		Roots: FilesystemRoots{InternalReadable: []string{memDir}},
	})

	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "read",
		Args:     mustJSON(t, map[string]any{"path": target}),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("deny rule must override internal-path allow, got %#v", decision)
	}
	if decision.Source != DecisionSourceRule {
		t.Fatalf("expected deny via rule, got source %q", decision.Source)
	}
}

func TestUserRootsTakePrecedenceOverInternal(t *testing.T) {
	// When a path is in BOTH the user's WriteRoots and InternalWritable,
	// the user-configured root wins: the request runs through the normal
	// mode-based flow (balanced → ask) instead of the internal silent allow.
	// User intent takes precedence over harness-declared internal paths so
	// the harness cannot silently override what the user explicitly opted
	// into. Lock this in so a future refactor can't quietly invert it.
	workspace := t.TempDir()
	memDir := t.TempDir()
	var prompted bool
	engine := newTestEngine(t, workspace, EngineConfig{
		Roots: FilesystemRoots{
			WriteRoots:       []string{memDir},
			InternalWritable: []string{memDir},
		},
		Approver: func(context.Context, Prompt) (Choice, error) {
			prompted = true
			return ChoiceAllowOnce, nil
		},
	})

	target := filepath.Join(memDir, "MEMORY.md")
	decision, err := engine.Decide(context.Background(), Request{
		ToolName: "write",
		Args:     mustJSON(t, map[string]any{"path": target}),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !prompted {
		t.Fatalf("expected approver to be called for user-write-roots path, got %#v", decision)
	}
	if decision == nil || !decision.Allowed() {
		t.Fatalf("expected allow after prompt, got %#v", decision)
	}
	if decision.Source == DecisionSourceInternal {
		t.Fatalf("internal silent allow must not preempt user-roots flow, got %#v", decision)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}

// A grant allows what the mode would ask about, for the request carrying it,
// and still yields to deny rules.
func TestGrantsAllowTheirRequestOnly(t *testing.T) {
	workspace := t.TempDir()
	rules, err := ParseRuleSet(nil, []string{"Bash(rm *)"})
	if err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t, workspace, EngineConfig{Rules: rules})
	grant, err := ParseRule("Bash")
	if err != nil {
		t.Fatal(err)
	}

	granted := toolReq("bash", map[string]any{"command": "go test ./..."})
	granted.Grants = []Rule{grant}
	if d, _ := engine.Decide(context.Background(), granted); !d.Allowed() || d.Source != DecisionSourceGrant {
		t.Fatalf("granted request: %#v", d)
	}
	if d, _ := engine.Decide(context.Background(), toolReq("bash", map[string]any{"command": "go test ./..."})); d.Allowed() {
		t.Fatalf("request without the grant was allowed: %#v", d)
	}
	denied := toolReq("bash", map[string]any{"command": "rm -rf build"})
	denied.Grants = []Rule{grant}
	if d, _ := engine.Decide(context.Background(), denied); d.Allowed() {
		t.Fatalf("grant beat a deny rule: %#v", d)
	}
}

// A classification asking to confirm the call asks every time, whatever the
// mode and the approvals given before, and an allow covers the call only.
func TestConfirmAsksEveryTime(t *testing.T) {
	workspace := t.TempDir()
	var prompts []Prompt
	engine := NewEngine(EngineConfig{
		Workspace: workspace,
		Mode:      ModeTrust,
		Classifier: func(req Request) Classification {
			c := testClassifier(req)
			c.Confirm = "shell startup file"
			return c
		},
		Approver: func(_ context.Context, p Prompt) (Choice, error) {
			prompts = append(prompts, p)
			return ChoiceAllowAlways, nil
		},
	})
	req := toolReq("write", map[string]any{"path": ".bashrc"})
	for range 2 {
		d, err := engine.Decide(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != DecisionAllowOnce {
			t.Fatalf("decision = %#v, want allow once", d)
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("asked %d times, want 2", len(prompts))
	}
	if p := prompts[0]; !p.OnceOnly || p.OutsideRoots || p.Reason != "shell startup file" {
		t.Fatalf("prompt = %#v", p)
	}
}
