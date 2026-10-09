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

type approveFunc func(context.Context, interact.Approval) (interact.Verdict, error)

func (f approveFunc) Approve(ctx context.Context, a interact.Approval) (interact.Verdict, error) {
	return f(ctx, a)
}

func (approveFunc) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}

// engineCfg is an Engine's Config with what one conversation brings.
type engineCfg struct {
	Cwd     string
	Rules   *RuleSet
	Roots   FilesystemRoots
	OnAudit func(AuditEntry)
	Mode    interact.Mode
	UI      interact.UI
}

// testEngine decides as the conversation of its engineCfg.
type testEngine struct {
	*Engine
	mode interact.Mode
	ui   interact.UI
}

func (e testEngine) Decide(ctx context.Context, req Request) (*Decision, error) {
	req.Mode, req.UI = e.mode, e.ui
	return e.Engine.Decide(ctx, req)
}

func newEngine(t *testing.T, cfg engineCfg) testEngine {
	t.Helper()
	if cfg.Cwd == "" {
		cfg.Cwd = t.TempDir()
	}
	t.Setenv("HOME", t.TempDir()) // approvals are stored under the home
	e, err := NewEngine(Config{Cwd: cfg.Cwd, Rules: cfg.Rules, Roots: cfg.Roots, OnAudit: cfg.OnAudit})
	if err != nil {
		t.Fatal(err)
	}
	return testEngine{e, cfg.Mode, cfg.UI}
}

func noGrants() []Rule { return nil }

func noMeta(string) Metadata { return Metadata{} }

// gate returns a function reporting whether e lets a call run.
func gate(e testEngine, grants func() []Rule, meta func(string) Metadata) func(ctx context.Context, name, args string) (bool, error) {
	mw := e.Middleware(Scope{Mode: func() interact.Mode { return e.mode }, UI: e.ui, Grants: grants, Meta: meta})
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

func TestMiddlewareResolvesPathsWhereTheToolRuns(t *testing.T) {
	main, wt := t.TempDir(), t.TempDir()
	var summaries []string
	e := newEngine(t, engineCfg{
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

func TestMiddlewareGrantsComeFromTheConversation(t *testing.T) {
	e := newEngine(t, engineCfg{Mode: interact.ModeBalanced})
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

// Neither trust mode nor an earlier "always" skips the prompt, but a deny
// rule still wins.
func TestDangerousPathsAreConfirmedEachTime(t *testing.T) {
	rules, err := ParseRuleSet(nil, []string{"Write(.git/hooks/*)"})
	if err != nil {
		t.Fatal(err)
	}
	var asked []interact.Approval
	e := newEngine(t, engineCfg{
		Mode:  interact.ModeTrust,
		Rules: rules,
		UI: approveFunc(func(_ context.Context, a interact.Approval) (interact.Verdict, error) {
			asked = append(asked, a)
			return interact.Verdict{Choice: interact.AllowAlways}, nil
		}),
	})
	decide := gate(e, noGrants, noMeta)
	for range 2 {
		if ran, err := decide(context.Background(), "edit", `{"file_path":".bashrc"}`); err != nil || !ran {
			t.Fatalf("ran %v, %v", ran, err)
		}
	}
	if len(asked) != 2 || !asked[0].Confirm || asked[0].Remember != "" || asked[0].OutsideRoots {
		t.Fatalf("asked %+v, want twice, once only, inside the roots", asked)
	}
	if ran, err := decide(context.Background(), "write", `{"file_path":".git/hooks/pre-commit"}`); err != nil || ran || len(asked) != 2 {
		t.Fatalf("deny rule: ran %v, %v, asked %d", ran, err, len(asked))
	}
}

func TestDecideAskUserHonorsDenyRules(t *testing.T) {
	rules, err := ParseRuleSet(nil, []string{"ask_user"})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := gate(newEngine(t, engineCfg{Rules: rules}), noGrants, noMeta)(context.Background(), "ask_user", `{"questions":[]}`); err != nil || ran {
		t.Fatalf("ran %v, %v", ran, err)
	}
	if ran, err := gate(newEngine(t, engineCfg{Mode: interact.ModeBalanced}), noGrants, noMeta)(context.Background(), "ask_user", `{"questions":[]}`); err != nil || !ran {
		t.Fatalf("ask_user is internal: ran %v, %v", ran, err)
	}
}

func TestMiddlewareUsesToolMetadata(t *testing.T) {
	e := newEngine(t, engineCfg{Mode: interact.ModeBalanced})
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

func TestMiddlewareApprovalCarriesTheCallAndTheWarning(t *testing.T) {
	var got interact.Approval
	e := newEngine(t, engineCfg{
		Mode: interact.ModeBalanced,
		UI: approveFunc(func(_ context.Context, a interact.Approval) (interact.Verdict, error) {
			got = a
			return interact.Verdict{Choice: interact.Deny}, nil
		}),
	})
	if _, err := gate(e, noGrants, noMeta)(context.Background(), "bash", `{"command":"git reset --hard HEAD~3"}`); err != nil {
		t.Fatal(err)
	}
	if got.ToolID != "call_1" || got.Warning == "" {
		t.Fatalf("approval = %+v, want the call ID and a warning", got)
	}
}
