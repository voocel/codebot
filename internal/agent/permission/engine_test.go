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

// Balanced mode lets a read in the workspace through and, with no one to
// ask, denies a write.
func TestBalancedWithoutUI(t *testing.T) {
	engine := newEngine(t, Config{})
	for tool, want := range map[string]DecisionKind{"read": DecisionAllow, "write": DecisionDeny} {
		decision, err := engine.Decide(context.Background(), toolReq(tool, map[string]any{"path": "a.txt"}))
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if decision == nil || decision.Kind != want || decision.Allowed() != (want == DecisionAllow) {
			t.Fatalf("%s: got %#v, want %s", tool, decision, want)
		}
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

	decision, err := engine.Decide(context.Background(), toolReq("read", map[string]any{"path": outside}))
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

// A denial tells the agent what the user said to do instead.
func TestDenialCarriesTheFeedback(t *testing.T) {
	engine := newEngine(t, Config{Cwd: t.TempDir(), UI: approveFunc(func(context.Context, interact.Approval) (interact.Verdict, error) {
		return interact.Verdict{Choice: interact.Deny, Feedback: "use make clean"}, nil
	})})
	d, err := engine.Decide(context.Background(), toolReq("bash", map[string]any{"command": "rm -rf build"}))
	if err != nil || d.Allowed() {
		t.Fatalf("%v, %v", d, err)
	}
	if !strings.Contains(d.Reason, "use make clean") {
		t.Errorf("reason %q", d.Reason)
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

// A harness-declared internal path is used without asking: a readable one
// for reads, a writable one for writes and reads both.
func TestInternalPathsSilentlyAllowed(t *testing.T) {
	for _, tc := range []struct {
		tool     string
		writable bool
	}{
		{"read", false},
		{"write", true},
		{"read", true},
	} {
		memDir := t.TempDir()
		roots := FilesystemRoots{InternalReadable: []string{memDir}}
		if tc.writable {
			roots = FilesystemRoots{InternalWritable: []string{memDir}}
		}
		engine := newEngine(t, Config{Cwd: t.TempDir(), Roots: roots, UI: mustNotAsk(t)})
		decision, err := engine.Decide(context.Background(), toolReq(tc.tool, map[string]any{"path": filepath.Join(memDir, "MEMORY.md")}))
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if decision == nil || !decision.Allowed() || decision.Source != DecisionSourceInternal {
			t.Fatalf("%s (writable %v): expected silent allow via internal path, got %#v", tc.tool, tc.writable, decision)
		}
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
	decision, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": target}))
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

	decision, err := engine.Decide(context.Background(), toolReq("read", map[string]any{"path": target}))
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
	decision, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": target}))
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

// A write in a protected directory, one whose files decide what codebot
// runs, is confirmed each time, though the mode lets edits through.
func TestProtectedWritesAskEachTime(t *testing.T) {
	workspace := t.TempDir()
	kit := filepath.Join(workspace, "kit")
	var asked []string
	engine := newEngine(t, Config{
		Cwd:   workspace,
		Mode:  interact.ModeAcceptEdits,
		Roots: FilesystemRoots{Protected: []string{kit}},
		UI: approveFunc(func(_ context.Context, p interact.Approval) (interact.Verdict, error) {
			asked = append(asked, p.Summary)
			return interact.Verdict{Choice: interact.AllowOnce}, nil
		}),
	})
	for _, path := range []string{filepath.Join(kit, "mcp.json"), filepath.Join(workspace, "main.go")} {
		if _, err := engine.Decide(context.Background(), toolReq("write", map[string]any{"path": path})); err != nil {
			t.Fatal(err)
		}
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "mcp.json") {
		t.Errorf("asked about %q, want the plugin's file alone", asked)
	}
}
