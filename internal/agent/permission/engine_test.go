package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/interact"
)

func mustNotAsk(t *testing.T) interact.UI {
	return approveFunc(func(context.Context, interact.Approval) (interact.Verdict, error) {
		t.Errorf("the user was asked")
		return interact.Verdict{Choice: interact.Deny}, nil
	})
}

func toolReq(name string, args map[string]any) Request {
	raw, _ := json.Marshal(args)
	return Request{
		ToolName: name,
		Args:     raw,
	}
}

func TestBalancedReadAllowed(t *testing.T) {
	engine := newEngine(t, Config{})

	decision, err := engine.Decide(context.Background(), toolReq("read", map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Kind != DecisionAllow || !decision.Allowed() {
		t.Fatalf("expected auto allow, got %#v", decision)
	}
}

func TestBalancedWriteDeniedWithoutUI(t *testing.T) {
	engine := newEngine(t, Config{})

	decision, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Kind != DecisionDeny || decision.Allowed() {
		t.Fatalf("expected deny without a UI, got %#v", decision)
	}
}

func TestOutsideRootsAllowsOnlyOnce(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	var prompt interact.Approval
	engine := newEngine(t, Config{Cwd: workspace, UI: approveFunc(func(_ context.Context, p interact.Approval) (interact.Verdict, error) {
		prompt = p
		return interact.Verdict{Choice: interact.AllowAlways}, nil
	})})

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
	if !prompt.OutsideRoots || !prompt.Confirm || prompt.Remember != "" {
		t.Fatalf("prompt = %#v, want it outside the roots, confirmed each time, with nothing to remember", prompt)
	}
}

// Always remembers the commands a call runs, each of which must be
// remembered for a later call to go unasked: one command approved does
// not let another ride along.
func TestAlwaysRemembersEachCommand(t *testing.T) {
	var asked []interact.Approval
	engine := newEngine(t, Config{Cwd: t.TempDir(), UI: approveFunc(func(_ context.Context, p interact.Approval) (interact.Verdict, error) {
		asked = append(asked, p)
		return interact.Verdict{Choice: interact.AllowAlways}, nil
	})})
	run := func(cmd string) {
		t.Helper()
		if d, err := engine.Decide(context.Background(), toolReq("bash", map[string]any{"command": cmd})); err != nil || !d.Allowed() {
			t.Fatalf("%s: %v, %v", cmd, d, err)
		}
	}
	run("go test ./...")
	if got := asked[0].Remember; got != "`go test` commands in this project" {
		t.Errorf("remember %q", got)
	}
	run("go test -run X ./... 2>&1 | tail -5")
	if len(asked) != 1 {
		t.Errorf("a remembered command was asked again: %+v", asked[1:])
	}
	run("go test ./... && curl -s x.example")
	if len(asked) != 2 || asked[1].Remember != "`go test` and `curl` commands in this project" {
		t.Errorf("asked %+v", asked)
	}
	// What cannot be remembered is asked every time.
	run("go test $(cat pkgs)")
	run("go test $(cat pkgs)")
	if len(asked) != 4 || asked[3].Remember != "" {
		t.Errorf("asked %+v", asked)
	}
}

// A denial tells the agent it was the user's, with what they said to do
// instead.
func TestDenialCarriesTheFeedback(t *testing.T) {
	feedback := ""
	engine := newEngine(t, Config{Cwd: t.TempDir(), UI: approveFunc(func(context.Context, interact.Approval) (interact.Verdict, error) {
		return interact.Verdict{Choice: interact.Deny, Feedback: feedback}, nil
	})})
	deny := func() string {
		t.Helper()
		d, err := engine.Decide(context.Background(), toolReq("bash", map[string]any{"command": "rm -rf build"}))
		if err != nil || d.Allowed() {
			t.Fatalf("%v, %v", d, err)
		}
		return d.Reason
	}
	if r := deny(); !strings.Contains(r, "The user denied this") {
		t.Errorf("reason %q", r)
	}
	feedback = "use make clean"
	if r := deny(); !strings.Contains(r, "use make clean") {
		t.Errorf("reason %q", r)
	}
}

// An edit offers the accept-edits mode instead of remembering the file.
func TestEditOffersTheMode(t *testing.T) {
	var prompt interact.Approval
	engine := newEngine(t, Config{Cwd: t.TempDir(), UI: approveFunc(func(_ context.Context, p interact.Approval) (interact.Verdict, error) {
		prompt = p
		return interact.Verdict{Choice: interact.AllowOnce}, nil
	})})
	if _, err := engine.Decide(context.Background(), toolReq("edit", map[string]any{"file_path": "a.go"})); err != nil {
		t.Fatal(err)
	}
	if !prompt.Edit || prompt.Remember != "" {
		t.Errorf("prompt = %+v", prompt)
	}
}

func TestPromptCarriesTheToolCallID(t *testing.T) {
	workspace := t.TempDir()
	var got string
	engine := newEngine(t, Config{Cwd: workspace, UI: approveFunc(func(_ context.Context, p interact.Approval) (interact.Verdict, error) {
		got = p.ToolID
		return interact.Verdict{Choice: interact.Deny}, nil
	})})
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

	engine := newEngine(t, Config{Cwd: workspace})
	decision, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": "link/escape.txt"}))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected denial for symlink escape, got %#v", decision)
	}
}

func TestMetadataOverrideForCustomTool(t *testing.T) {
	engine := newEngine(t, Config{})

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
	if decision == nil || decision.Kind != DecisionAllow || decision.Capability != CapabilityRead {
		t.Fatalf("expected metadata-driven allow, got %#v", decision)
	}
}

func TestInternalReadablePathSilentlyAllowed(t *testing.T) {
	workspace := t.TempDir()
	memDir := t.TempDir()
	engine := newEngine(t, Config{
		Cwd:   workspace,
		Roots: FilesystemRoots{InternalReadable: []string{memDir}},
		UI:    mustNotAsk(t),
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
	engine := newEngine(t, Config{
		Cwd:   workspace,
		Roots: FilesystemRoots{InternalWritable: []string{memDir}},
		UI:    mustNotAsk(t),
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
	engine := newEngine(t, Config{
		Cwd:   workspace,
		Roots: FilesystemRoots{InternalWritable: []string{memDir}},
		UI:    mustNotAsk(t),
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
	engine := newEngine(t, Config{
		Cwd:   workspace,
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
	engine := newEngine(t, Config{
		Cwd:   workspace,
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
	engine := newEngine(t, Config{
		Cwd: workspace,
		Roots: FilesystemRoots{
			WriteRoots:       []string{memDir},
			InternalWritable: []string{memDir},
		},
		UI: approveFunc(func(context.Context, interact.Approval) (interact.Verdict, error) {
			prompted = true
			return interact.Verdict{Choice: interact.AllowOnce}, nil
		}),
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
		t.Fatalf("expected the user to be asked for user-write-roots path, got %#v", decision)
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
	engine := newEngine(t, Config{Cwd: workspace, Rules: rules})
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
