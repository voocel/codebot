package permission

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/interact"
)

// approveFunc is a UI that answers approvals with itself.
type approveFunc func(context.Context, interact.Approval) (interact.Choice, error)

func (f approveFunc) Approve(ctx context.Context, a interact.Approval) (interact.Choice, error) {
	return f(ctx, a)
}

func (approveFunc) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}

func newEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	if cfg.Cwd == "" {
		cfg.Cwd = t.TempDir()
	}
	t.Setenv("HOME", t.TempDir()) // approvals are stored under the home
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func noGrants() []Rule { return nil }

func noMeta(string) Metadata { return Metadata{} }

// gate returns a function reporting whether e lets a call run.
func gate(e *Engine, grants func() []Rule, meta func(string) Metadata) func(ctx context.Context, name, args string) (bool, error) {
	mw := e.Middleware(grants, meta)
	return func(ctx context.Context, name, args string) (bool, error) {
		ran := false
		tool := agentcore.Tool{Name: name}
		_, err := mw(ctx, agentcore.ToolCall{ID: "call_1", Name: name, Args: json.RawMessage(args), Tool: &tool},
			func(context.Context, agentcore.ToolCall) (agentcore.Result, error) {
				ran = true
				return agentcore.TextResult("ran"), nil
			})
		return ran, err
	}
}

// Paths resolve against the directory the tool runs in, carried on the
// context, so a worktree's files are checked where they are.
func TestMiddlewareResolvesPathsWhereTheToolRuns(t *testing.T) {
	main, wt := t.TempDir(), t.TempDir()
	var summaries []string
	e := newEngine(t, Config{
		Cwd:     main,
		Mode:    interact.ModeTrust,
		Roots:   FilesystemRoots{ReadRoots: []string{main, wt}, WriteRoots: []string{main, wt}},
		OnAudit: func(a AuditEntry) { summaries = append(summaries, a.Summary) },
	})
	decide := gate(e, noGrants, noMeta)
	for _, ctx := range []context.Context{context.Background(), agentcoretools.WithCwd(context.Background(), func() string { return wt })} {
		if _, err := decide(ctx, "write", `{"file_path":"a.txt"}`); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{filepath.Join(main, "a.txt"), filepath.Join(wt, "a.txt")}
	if len(summaries) != 2 || summaries[0] != want[0] || summaries[1] != want[1] {
		t.Fatalf("audited %v, want %v", summaries, want)
	}
}

// A conversation's grants allow its calls only while it hands them in.
func TestMiddlewareGrantsComeFromTheConversation(t *testing.T) {
	e := newEngine(t, Config{Mode: interact.ModeBalanced})
	var grants []Rule
	decide := gate(e, func() []Rule { return grants }, noMeta)
	run := func() bool {
		ran, err := decide(context.Background(), "bash", `{"command":"go test ./..."}`)
		if err != nil {
			t.Fatal(err)
		}
		return ran
	}
	if run() {
		t.Fatal("allowed without a grant or a UI")
	}
	grants = ParseGrants([]string{"Bash(go test *)", "not a rule("})
	if !run() {
		t.Fatal("the grant did not allow the call")
	}
	grants = nil
	if run() {
		t.Fatal("allowed after the grant was dropped")
	}
}

// A dangerous path is confirmed every time, even in trust mode and after
// "always", but a deny rule still wins.
func TestDangerousPathsAreConfirmedEachTime(t *testing.T) {
	rules, err := ParseRuleSet(nil, []string{"Write(.git/hooks/*)"})
	if err != nil {
		t.Fatal(err)
	}
	var asked []interact.Approval
	e := newEngine(t, Config{
		Mode:  interact.ModeTrust,
		Rules: rules,
		UI: approveFunc(func(_ context.Context, a interact.Approval) (interact.Choice, error) {
			asked = append(asked, a)
			return interact.AllowAlways, nil
		}),
	})
	decide := gate(e, noGrants, noMeta)
	for range 2 {
		if ran, err := decide(context.Background(), "edit", `{"file_path":".bashrc"}`); err != nil || !ran {
			t.Fatalf("ran %v, %v", ran, err)
		}
	}
	if len(asked) != 2 || !asked[0].OnceOnly || asked[0].OutsideRoots {
		t.Fatalf("asked %+v, want twice, once only, inside the roots", asked)
	}
	if ran, err := decide(context.Background(), "write", `{"file_path":".git/hooks/pre-commit"}`); err != nil || ran || len(asked) != 2 {
		t.Fatalf("deny rule: ran %v, %v, asked %d", ran, err, len(asked))
	}
}

func TestApproveHookAllowAlwaysPersists(t *testing.T) {
	calls := 0
	e := newEngine(t, Config{
		Mode: interact.ModeBalanced,
		UI: approveFunc(func(context.Context, interact.Approval) (interact.Choice, error) {
			calls++
			return interact.AllowAlways, nil
		}),
	})
	req := HookRequest{Event: "PreToolUse", Tool: "bash", Command: "echo ok", Blocking: true}
	for range 2 {
		if err := e.ApproveHook(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("asked %d times, want 1", calls)
	}
}

func TestApproveHookFollowsTheMode(t *testing.T) {
	req := HookRequest{Event: "Stop", Command: "make lint"}
	if err := newEngine(t, Config{Mode: interact.ModeTrust}).ApproveHook(context.Background(), req); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if err := newEngine(t, Config{Mode: interact.ModeStrict}).ApproveHook(context.Background(), req); err == nil {
		t.Fatal("strict allowed a hook")
	}
	if err := newEngine(t, Config{Mode: interact.ModeBalanced}).ApproveHook(context.Background(), req); err == nil {
		t.Fatal("balanced allowed a hook nobody approved")
	}
}

func TestDecideAskUserHonorsDenyRules(t *testing.T) {
	rules, err := ParseRuleSet(nil, []string{"ask_user"})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := gate(newEngine(t, Config{Rules: rules}), noGrants, noMeta)(context.Background(), "ask_user", `{"questions":[]}`); err != nil || ran {
		t.Fatalf("ran %v, %v", ran, err)
	}
	if ran, err := gate(newEngine(t, Config{Mode: interact.ModeBalanced}), noGrants, noMeta)(context.Background(), "ask_user", `{"questions":[]}`); err != nil || !ran {
		t.Fatalf("ask_user is internal: ran %v, %v", ran, err)
	}
}

// A tool that classifies itself, as an MCP tool does, is decided by what it
// declares.
func TestMiddlewareUsesToolMetadata(t *testing.T) {
	e := newEngine(t, Config{Mode: interact.ModeBalanced})
	for capability, want := range map[Capability]bool{
		CapabilityRead:  true,
		CapabilityWrite: false, // asked, and nobody answers
	} {
		meta := func(name string) Metadata {
			if name != "mcp__srv__lookup" {
				t.Fatalf("metadata asked for %q", name)
			}
			return Metadata{Capability: capability, KeyPrefix: "mcp"}
		}
		if ran, err := gate(e, noGrants, meta)(context.Background(), "mcp__srv__lookup", `{}`); err != nil || ran != want {
			t.Errorf("%s: ran %v, %v", capability, ran, err)
		}
	}
}

// A tool call's approval names the call and warns about a destructive command.
func TestMiddlewareApprovalCarriesTheCallAndTheWarning(t *testing.T) {
	var got interact.Approval
	e := newEngine(t, Config{
		Mode: interact.ModeBalanced,
		UI: approveFunc(func(_ context.Context, a interact.Approval) (interact.Choice, error) {
			got = a
			return interact.Deny, nil
		}),
	})
	if _, err := gate(e, noGrants, noMeta)(context.Background(), "bash", `{"command":"git reset --hard HEAD~3"}`); err != nil {
		t.Fatal(err)
	}
	if got.ToolID != "call_1" || got.Warning == "" {
		t.Fatalf("approval = %+v, want the call ID and a warning", got)
	}
}
