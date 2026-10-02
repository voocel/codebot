package commands

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/provider"
)

// okModel is a provider answering every call with "ok".
type okModel struct{}

func (okModel) Name() string { return "ok" }

func (okModel) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	return litellmtest.New(litellmtest.Text("ok")).Chat(ctx, req)
}

func (okModel) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	return litellmtest.New(litellmtest.Text("ok")).Stream(ctx, req)
}

func newOKModel(spec provider.ModelSpec) (agentcore.Model, error) {
	client, err := litellm.New(okModel{})
	return agentcore.Model{Client: client, Request: litellm.Request{Model: spec.Model}}, err
}

type denyUI struct{}

func (denyUI) Approve(context.Context, interact.Approval) (interact.Choice, error) {
	return interact.Deny, nil
}

func (denyUI) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}

// boot starts an App on cwd with a throwaway home.
func boot(t *testing.T, cwd string) *app.App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := `{"provider":"anthropic","model":"claude-haiku-4-5","snapshot":false,
		"providers":{"anthropic":{"api_key":"test","models":["claude-haiku-4-5"]}}}`
	if err := os.MkdirAll(filepath.Join(home, ".codebot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codebot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Boot(app.Options{
		Cwd:      cwd,
		Mode:     interact.ModeBalanced,
		UI:       denyUI{},
		NewModel: newOKModel,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// emptyTable is a Table with no commands and no App.
func emptyTable() *Table {
	return &Table{
		aliases:       make(map[string]Command),
		entries:       make(map[string]Command),
		activeAliases: make(map[string][]string),
	}
}

func run(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestParseInvocationRespectsQuotes(t *testing.T) {
	t.Parallel()

	inv, ok := ParseInvocation(`/commit "feat scope" --amend`)
	if !ok {
		t.Fatal("expected command to parse")
	}
	if inv.Name != "commit" || inv.RawArgs != `"feat scope" --amend` {
		t.Fatalf("name, raw args = %q, %q", inv.Name, inv.RawArgs)
	}
	if want := []string{"feat scope", "--amend"}; !slices.Equal(inv.Args, want) {
		t.Fatalf("args = %v", inv.Args)
	}
}

func TestCanonicalNameWinsOverAConflictingAlias(t *testing.T) {
	t.Parallel()

	table := emptyTable()
	custom := NewSimple(Spec{Name: "q", Kind: KindSkill}, nil)
	builtin := NewSimple(Spec{Name: "exit", Aliases: []string{"q"}, Kind: KindBuiltin}, nil)
	table.Register(custom)
	table.Register(builtin)

	if got, ok := table.Lookup("q"); !ok || got != custom {
		t.Fatalf("expected canonical command q to win, got %#v", got)
	}
	if aliases := table.EffectiveSpec(builtin).Aliases; len(aliases) != 0 {
		t.Fatalf("expected conflicting alias to be hidden, got %v", aliases)
	}
}

func TestAliasMovesToItsLatestOwner(t *testing.T) {
	t.Parallel()

	table := emptyTable()
	first := NewSimple(Spec{Name: "deploy", Aliases: []string{"ship"}, Kind: KindBuiltin}, nil)
	second := NewSimple(Spec{Name: "release", Aliases: []string{"ship"}, Kind: KindBuiltin}, nil)
	table.Register(first)
	table.Register(second)

	if got, ok := table.Lookup("ship"); !ok || got != second {
		t.Fatalf("expected latest alias owner, got %#v", got)
	}
	if aliases := table.EffectiveSpec(first).Aliases; len(aliases) != 0 {
		t.Fatalf("expected old owner alias to be hidden, got %v", aliases)
	}
	if aliases := table.EffectiveSpec(second).Aliases; !slices.Equal(aliases, []string{"ship"}) {
		t.Fatalf("expected new owner alias to remain active, got %v", aliases)
	}
}

func TestPaletteMatchesNamesAndAliasesOnly(t *testing.T) {
	table := New(boot(t, t.TempDir()), "test")

	items := table.Complete("quit")
	if len(items) == 0 || items[0].Name != "exit" {
		t.Fatalf("expected alias query to resolve exit, got %#v", items)
	}
	if items := table.Complete("toggle"); len(items) != 0 {
		t.Fatalf("expected description query to NOT match, got %#v", items)
	}
}

func TestPaletteAutoExecutesCommandsWithoutArguments(t *testing.T) {
	t.Parallel()

	if !paletteItem(Spec{Name: "help", Usage: "/help"}).AutoExecute {
		t.Fatal("expected no-arg command to auto execute")
	}
	if paletteItem(Spec{Name: "model", Usage: "/model [name]"}).AutoExecute {
		t.Fatal("expected arg command to only fill input")
	}
}

// Overlay commands hold the table; a rebuild after /reload must keep it and
// the overlay on screen.
func TestRebuildKeepsTheOpenOverlay(t *testing.T) {
	table := New(boot(t, t.TempDir()), "test")

	run(table.Run("/help"))
	if table.Overlay() == nil {
		t.Fatal("expected /help to open its overlay")
	}
	table.Rebuild()
	if table.Overlay() == nil {
		t.Fatal("rebuild must keep the open overlay")
	}
}

func TestSkillsBecomeCommandsWhenTheyApply(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "internal", "skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(writePlugin(t, cwd, "kit"), "skills")
	for name, body := range map[string]string{
		"backend.md":  "---\ndescription: Backend skill\npaths:\n  - internal/skill/**\n---\nReview code",
		"frontend.md": "---\ndescription: Frontend skill\npaths:\n  - web/**\n---\nBuild UI",
	} {
		if err := os.WriteFile(filepath.Join(skills, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	table := New(boot(t, cwd), "test")
	if _, ok := table.Lookup("backend"); !ok {
		t.Fatal("expected the applicable skill to be a command")
	}
	if _, ok := table.Lookup("frontend"); ok {
		t.Fatal("expected the path-scoped skill to stay out")
	}
	if _, ok := table.Lookup("help"); !ok {
		t.Fatal("expected built-in commands beside the skills")
	}
}

func TestSkillCommandSubmitsAnInlineSkill(t *testing.T) {
	cwd := t.TempDir()
	skills := filepath.Join(writePlugin(t, cwd, "kit"), "skills")
	if err := os.WriteFile(filepath.Join(skills, "greet.md"), []byte("---\ndescription: Greet\n---\nSay hello to $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := boot(t, cwd)
	table := New(a, "test")

	if msg := run(table.Run("/greet world")); msg != nil {
		t.Fatalf("inline skill returned %#v, want it submitted", msg)
	}
	conv := a.Current()
	if err := conv.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	history := conv.History()
	if len(history) != 2 || !strings.Contains(history[0].Text(), "Say hello to world") {
		t.Fatalf("history = %+v, want the skill prompt and a reply", history)
	}
}

// writePlugin creates a project plugin with a skills directory.
func writePlugin(t *testing.T, cwd, id string) string {
	t.Helper()
	root := filepath.Join(cwd, ".codebot", "plugins", id)
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"` + id + `","name":"` + id + `","version":"0.1.0","skillsDir":"./skills"}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func plugins(t *testing.T, a *app.App, args ...string) tea.Msg {
	t.Helper()
	cmd := &PluginsCommand{app: a, table: New(a, "test")}
	msg := run(cmd.Run(Invocation{Name: "plugins", Args: args}))
	if msg == nil {
		t.Fatal("expected a command result")
	}
	return msg
}

func loadedPlugin(t *testing.T, cwd, id string) (plugin.Loaded, bool) {
	t.Helper()
	catalog, err := plugin.LoadAll(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, loaded := range catalog.Plugins() {
		if loaded.Manifest.ID == id {
			return loaded, true
		}
	}
	return plugin.Loaded{}, false
}

func TestPluginsListsAndShows(t *testing.T) {
	cwd := t.TempDir()
	writePlugin(t, cwd, "docs")
	a := boot(t, cwd)

	plugins(t, a)
	plugins(t, a, "list")
	plugins(t, a, "show", "docs")
	plugins(t, a, "show", "missing")
}

func TestPluginsDisableAndTrustWriteTheirState(t *testing.T) {
	cwd := t.TempDir()
	writePlugin(t, cwd, "docs")
	a := boot(t, cwd)

	plugins(t, a, "disable", "docs")
	if loaded, ok := loadedPlugin(t, cwd, "docs"); !ok || loaded.State.Enabled {
		t.Fatalf("docs = %+v, want it present and disabled", loaded.State)
	}
	plugins(t, a, "trust", "docs", "untrusted")
	if loaded, _ := loadedPlugin(t, cwd, "docs"); loaded.State.Trust != plugin.TrustUntrusted {
		t.Fatalf("trust = %q", loaded.State.Trust)
	}
}

func TestPluginsCreateInstallValidateRemove(t *testing.T) {
	cwd := t.TempDir()
	a := boot(t, cwd)

	plugins(t, a, "create", "review-helper")
	if _, err := os.Stat(filepath.Join(cwd, ".codebot", "plugins", "review-helper", "plugin.json")); err != nil {
		t.Fatalf("created manifest missing: %v", err)
	}
	plugins(t, a, "create", "other", "team") // bad scope is reported

	src := filepath.Join(t.TempDir(), "ops-kit")
	if err := os.MkdirAll(filepath.Join(src, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{"id":"ops-kit","name":"Ops Kit","version":"0.1.0","skillsDir":"./skills"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plugins(t, a, "validate", src)
	plugins(t, a, "install", src)
	installed := filepath.Join(cwd, ".codebot", "plugins", "ops-kit")
	if _, err := os.Stat(filepath.Join(installed, "plugin.json")); err != nil {
		t.Fatalf("installed manifest missing: %v", err)
	}
	plugins(t, a, "remove", "ops-kit")
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("removed plugin still on disk: %v", err)
	}
}
