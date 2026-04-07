package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/apperr"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/ui/tui"
)

type uiStubChatModel struct{}

func (m *uiStubChatModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{
		Message: agentcore.Message{
			Role:       agentcore.RoleAssistant,
			Content:    []agentcore.ContentBlock{agentcore.TextBlock("ok")},
			StopReason: agentcore.StopReasonStop,
		},
	}, nil
}

func (m *uiStubChatModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ok")}},
		StopReason: agentcore.StopReasonStop,
	}
	close(ch)
	return ch, nil
}

func (m *uiStubChatModel) SupportsTools() bool { return true }

type uiExecTool struct {
	name string
	run  func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

func (t *uiExecTool) Name() string           { return t.name }
func (t *uiExecTool) Description() string    { return "test tool" }
func (t *uiExecTool) Schema() map[string]any { return nil }
func (t *uiExecTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return t.run(ctx, args)
}

func TestValidateCommandRequiresIdleWhenRunning(t *testing.T) {
	t.Parallel()

	spec := CommandSpec{
		Category:  "info",
		NeedsIdle: true,
	}
	if err := validateCommand(context.Background(), nil, spec, true); err == nil {
		t.Fatalf("expected running agent command to be denied")
	}
}

func TestValidateCommandAllowsInfoCommandWhenRunning(t *testing.T) {
	t.Parallel()

	spec := CommandSpec{
		Category:  "info",
		NeedsIdle: false,
	}
	if err := validateCommand(context.Background(), nil, spec, true); err != nil {
		t.Fatalf("expected info command to pass while running: %v", err)
	}
}

func TestValidateCommandBlocksSessionCommandInPlanMode(t *testing.T) {
	t.Parallel()

	engine, err := approval.NewEngine(t.TempDir(), "balanced", nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanMode(true)

	spec := CommandSpec{
		Name:      "new",
		Category:  "session",
		NeedsIdle: false,
	}
	if err := validateCommand(context.Background(), engine, spec, false); err == nil {
		t.Fatalf("expected plan mode to deny session command")
	}
}

func TestValidatePluginMutationRequiresIdle(t *testing.T) {
	t.Parallel()

	if err := validatePluginMutation(true); err == nil {
		t.Fatal("expected running agent mutation to be denied")
	}
	if err := validatePluginMutation(false); err != nil {
		t.Fatalf("expected idle mutation to pass: %v", err)
	}
}

func TestParseCommandInvocationRespectsQuotes(t *testing.T) {
	t.Parallel()

	inv, ok := parseCommandInvocation(`/commit "feat scope" --amend`)
	if !ok {
		t.Fatal("expected command to parse")
	}
	if inv.Name != "commit" {
		t.Fatalf("unexpected command name: %q", inv.Name)
	}
	if inv.RawArgs != `"feat scope" --amend` {
		t.Fatalf("unexpected raw args: %q", inv.RawArgs)
	}
	want := []string{"feat scope", "--amend"}
	if !slices.Equal(inv.Args, want) {
		t.Fatalf("unexpected args: %v", inv.Args)
	}
}

func TestRebuildRegistryIncludesAllCommandSources(t *testing.T) {
	t.Parallel()

	app := &App{
		Commands: []config.FileCommand{
			{Name: "deploy", Description: "Deploy project"},
		},
		Skills: []skill.Spec{
			{Name: "review", Description: "Code review skill"},
		},
	}

	app.rebuildRegistry()

	for _, name := range []string{"help", "context", "debug-harness", "deploy", "review"} {
		if _, ok := app.registry.Lookup(name); !ok {
			t.Fatalf("expected command %q to be registered", name)
		}
	}
}

func TestRebuildRegistryUsesActiveSkillsFromCatalog(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "internal", "skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(cwd, ".codebot", "skills")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "review.md"), []byte("---\ndescription: Code review skill\npaths:\n  - internal/skill/**\n---\nReview code"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "frontend.md"), []byte("---\ndescription: Frontend skill\npaths:\n  - web/**\n---\nBuild UI"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{
		Cwd:          cwd,
		SkillCatalog: skill.NewCatalog(cwd, nil, skill.DirSource{Path: skillDir, Source: "plugin"}),
	}
	app.rebuildRegistry()
	if _, ok := app.registry.Lookup("review"); !ok {
		t.Fatal("expected active skill command to be registered")
	}
	if _, ok := app.registry.Lookup("frontend"); ok {
		t.Fatal("expected inactive path-scoped skill to stay out of the registry")
	}
}

func TestRegistryPreservesCanonicalNameOverConflictingAlias(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	custom := NewSimple(CommandSpec{Name: "q", Kind: CommandKindCustom}, nil)
	builtin := NewSimple(CommandSpec{Name: "exit", Aliases: []string{"q"}, Kind: CommandKindBuiltin}, nil)

	reg.Register(custom)
	reg.Register(builtin)

	got, ok := reg.Lookup("q")
	if !ok || got != custom {
		t.Fatalf("expected canonical command q to win, got %#v", got)
	}

	spec := reg.EffectiveSpec(builtin)
	if len(spec.Aliases) != 0 {
		t.Fatalf("expected conflicting alias to be hidden, got %v", spec.Aliases)
	}
}

func TestCmdPluginsShowsLoadedPlugins(t *testing.T) {
	t.Parallel()

	app := &App{
		PluginCatalog: &plugin.Catalog{},
	}
	if msg := app.cmdPlugins(nil)(); msg == nil {
		t.Fatal("expected plugin command to return a message")
	}
}

func TestCmdPluginsDisableWritesStateAndReloadsCatalog(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	pluginRoot := filepath.Join(cwd, ".codebot", "plugins", "docs")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"id":"docs","name":"Docs","version":"0.1.0","skillsDir":"./skills"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := plugin.LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	app := &App{Cwd: cwd, PluginCatalog: catalog}

	if msg := app.cmdPlugins([]string{"disable", "docs"})(); msg == nil {
		t.Fatal("expected command result")
	}

	reloaded, err := plugin.LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll after disable: %v", err)
	}
	found := false
	for _, loaded := range reloaded.Plugins() {
		if loaded.Manifest.ID == "docs" {
			found = true
			if loaded.State.Enabled {
				t.Fatal("expected docs plugin to be disabled")
			}
		}
	}
	if !found {
		t.Fatal("expected docs plugin to still exist after disable")
	}
}

func TestCmdPluginsTrustWritesStateAndReloadsCatalog(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	pluginRoot := filepath.Join(cwd, ".codebot", "plugins", "ops")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"id":"ops","name":"Ops","version":"0.1.0","skillsDir":"./skills"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := plugin.LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	app := &App{Cwd: cwd, PluginCatalog: catalog}

	if msg := app.cmdPlugins([]string{"trust", "ops", "untrusted"})(); msg == nil {
		t.Fatal("expected command result")
	}

	reloaded, err := plugin.LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll after trust: %v", err)
	}
	found := false
	for _, loaded := range reloaded.Plugins() {
		if loaded.Manifest.ID == "ops" {
			found = true
			if loaded.State.Trust != plugin.TrustUntrusted {
				t.Fatalf("expected ops plugin trust to be untrusted, got %q", loaded.State.Trust)
			}
		}
	}
	if !found {
		t.Fatal("expected ops plugin to still exist after trust change")
	}
}

func TestCmdPluginsShowRejectsUnknownPlugin(t *testing.T) {
	t.Parallel()

	app := &App{
		PluginCatalog: &plugin.Catalog{},
	}
	if msg := app.cmdPlugins([]string{"show", "missing"})(); msg == nil {
		t.Fatal("expected command result")
	}
}

func TestCmdPluginsCreateScaffoldsProjectPlugin(t *testing.T) {
	cwd := t.TempDir()
	app := &App{Cwd: cwd, PluginCatalog: &plugin.Catalog{}}

	msg := app.cmdPlugins([]string{"create", "review-helper"})()
	if msg == nil {
		t.Fatal("expected command result")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".codebot", "plugins", "review-helper", "plugin.json")); err != nil {
		t.Fatalf("plugin manifest missing: %v", err)
	}
}

func TestCmdPluginsCreateRejectsBadScope(t *testing.T) {
	t.Parallel()

	app := &App{Cwd: t.TempDir(), PluginCatalog: &plugin.Catalog{}}
	if msg := app.cmdPlugins([]string{"create", "review-helper", "team"})(); msg == nil {
		t.Fatal("expected command result")
	}
}

func TestCmdPluginsValidatePath(t *testing.T) {
	cwd := t.TempDir()
	root := filepath.Join(cwd, "review-assistant")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{"id":"review-assistant","name":"Review Assistant","version":"0.1.0","skillsDir":"./skills"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "review.md"), []byte("---\ndescription: review\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{Cwd: cwd, PluginCatalog: &plugin.Catalog{}}
	if msg := app.cmdPlugins([]string{"validate", root})(); msg == nil {
		t.Fatal("expected command result")
	}
}

func TestCmdPluginsInstallCopiesPluginIntoProjectScope(t *testing.T) {
	cwd := t.TempDir()
	src := filepath.Join(cwd, "external-plugin")
	if err := os.MkdirAll(filepath.Join(src, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{"id":"ops-kit","name":"Ops Kit","version":"0.1.0","skillsDir":"./skills"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{Cwd: cwd, PluginCatalog: &plugin.Catalog{}}
	if msg := app.cmdPlugins([]string{"install", src})(); msg == nil {
		t.Fatal("expected command result")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".codebot", "plugins", "ops-kit", "plugin.json")); err != nil {
		t.Fatalf("installed plugin manifest missing: %v", err)
	}
}

func TestCmdPluginsRemoveDeletesPlugin(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	pluginRoot := filepath.Join(cwd, ".codebot", "plugins", "docs")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"id":"docs","name":"Docs","version":"0.1.0","skillsDir":"./skills"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := plugin.LoadAll(cwd)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	app := &App{Cwd: cwd, PluginCatalog: catalog}

	if msg := app.cmdPlugins([]string{"remove", "docs"})(); msg == nil {
		t.Fatal("expected command result")
	}
	if _, err := os.Stat(pluginRoot); !os.IsNotExist(err) {
		t.Fatalf("expected plugin dir removed, got err=%v", err)
	}
}

func TestCmdPluginsListSubcommand(t *testing.T) {
	t.Parallel()

	app := &App{
		PluginCatalog: &plugin.Catalog{},
	}
	if msg := app.cmdPlugins([]string{"list"})(); msg == nil {
		t.Fatal("expected command result")
	}
}

func TestRegistryReassignsAliasAndHidesOldOwnerAlias(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	first := NewSimple(CommandSpec{Name: "deploy", Aliases: []string{"ship"}, Kind: CommandKindCustom}, nil)
	second := NewSimple(CommandSpec{Name: "release", Aliases: []string{"ship"}, Kind: CommandKindCustom}, nil)

	reg.Register(first)
	reg.Register(second)

	got, ok := reg.Lookup("ship")
	if !ok || got != second {
		t.Fatalf("expected latest alias owner, got %#v", got)
	}

	if aliases := reg.EffectiveSpec(first).Aliases; len(aliases) != 0 {
		t.Fatalf("expected old owner alias to be hidden, got %v", aliases)
	}
	if aliases := reg.EffectiveSpec(second).Aliases; !slices.Equal(aliases, []string{"ship"}) {
		t.Fatalf("expected new owner alias to remain active, got %v", aliases)
	}
}

func TestCommandPaletteMatchesAliasAndDescription(t *testing.T) {
	t.Parallel()

	app := &App{
		Commands: []config.FileCommand{
			{
				Name:        "deploy",
				Aliases:     []string{"ship"},
				Description: "Deploy project to staging",
				Usage:       "/deploy [env]",
			},
		},
	}
	app.rebuildRegistry()

	aliasItems := app.completions("ship")
	if len(aliasItems) == 0 || aliasItems[0].Name != "deploy" {
		t.Fatalf("expected alias query to resolve deploy, got %#v", aliasItems)
	}
	if aliasItems[0].Usage != "/deploy [env]" {
		t.Fatalf("expected usage metadata to be preserved, got %q", aliasItems[0].Usage)
	}

	// Description is intentionally NOT matched — only command name and aliases.
	descItems := app.completions("staging")
	if len(descItems) != 0 {
		t.Fatalf("expected description query to NOT match, got %#v", descItems)
	}
}

func TestCommandPaletteAutoExecuteDependsOnUsage(t *testing.T) {
	t.Parallel()

	noArgs, _, _ := buildCommandPaletteItem(CommandSpec{
		Name:  "help",
		Usage: "/help",
	}, "")
	if !noArgs.AutoExecute {
		t.Fatal("expected no-arg command to auto execute")
	}

	withArgs, _, _ := buildCommandPaletteItem(CommandSpec{
		Name:  "model",
		Usage: "/model [name]",
	}, "")
	if withArgs.AutoExecute {
		t.Fatal("expected arg command to only fill input")
	}
}

func TestSkillCommandRunsForkedSkillDirectlyForUserInvocation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var captured map[string]string
	subagent := &uiExecTool{
		name: "subagent",
		run: func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
			if err := json.Unmarshal(args, &captured); err != nil {
				t.Fatalf("unmarshal subagent args: %v", err)
			}
			return json.Marshal(map[string]any{
				"output": "forked result",
			})
		},
	}

	sess := agent.NewSession(agent.SessionConfig{
		Agent:    agentcore.NewAgent(agentcore.WithModel(&uiStubChatModel{})),
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      dir,
		Tools:    []agentcore.Tool{subagent},
	})
	t.Cleanup(sess.Close)

	app := &App{
		Session: sess,
		Cwd:     dir,
		SkillCatalog: skill.NewStaticCatalog([]skill.Spec{
			{
				Name:                   "deep-review",
				Description:            "Forked review",
				Context:                "fork",
				Agent:                  "plan",
				Model:                  "openai/gpt-5",
				DisableModelInvocation: true,
				GetPrompt: func(_ context.Context, args string, _ string) (string, error) {
					return "fork task: " + args, nil
				},
			},
		}),
	}

	cmd := (&SkillCommand{skill: skill.Spec{Name: "deep-review"}}).Run(&CommandContext{App: app}, CommandInvocation{
		Name:    "deep-review",
		RawArgs: "audit auth flow",
	})

	msg := cmd()
	result, ok := msg.(tui.CommandResultMsg)
	if !ok {
		t.Fatalf("expected CommandResultMsg, got %T", msg)
	}
	if !strings.Contains(result.Text, "forked result") {
		t.Fatalf("expected rendered subagent output, got %q", result.Text)
	}
	if captured["agent"] != "plan" {
		t.Fatalf("expected normalized fork agent, got %#v", captured)
	}
	if captured["task"] != "fork task: audit auth flow" {
		t.Fatalf("expected direct fork task execution, got %#v", captured)
	}
	if captured["model"] != "openai/gpt-5" {
		t.Fatalf("expected model override to be forwarded, got %#v", captured)
	}
}

func TestSkillCommandRunsInlineSkillThroughUnifiedExecutor(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sess := agent.NewSession(agent.SessionConfig{
		Agent:    agentcore.NewAgent(agentcore.WithModel(&uiStubChatModel{})),
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      dir,
	})
	t.Cleanup(sess.Close)

	app := &App{
		Session: sess,
		Cwd:     dir,
		SkillCatalog: skill.NewStaticCatalog([]skill.Spec{
			{
				Name:        "review",
				Description: "Inline review",
				GetPrompt: func(_ context.Context, args string, _ string) (string, error) {
					return "review task: " + args, nil
				},
			},
		}),
	}

	cmd := (&SkillCommand{skill: skill.Spec{Name: "review"}}).Run(&CommandContext{App: app}, CommandInvocation{
		Name:    "review",
		RawArgs: "auth flow",
	})

	msg := cmd()
	prompt, ok := msg.(tui.PromptMsg)
	if !ok {
		t.Fatalf("expected PromptMsg, got %T", msg)
	}
	if prompt.Text != "review task: auth flow" {
		t.Fatalf("unexpected prompt text: %#v", prompt)
	}
}

func TestFormatAutoCompactionEventIgnoresManual(t *testing.T) {
	t.Parallel()

	if text, _, ok := formatAutoCompactionEvent(agent.SessionEvent{
		Type:             agent.SEAutoCompactionStart,
		CompactionReason: "manual",
	}); ok || text != "" {
		t.Fatalf("expected manual compaction event to be ignored, got ok=%v text=%q", ok, text)
	}
}

func TestFormatAutoCompactionEventReportsResult(t *testing.T) {
	t.Parallel()

	text, muted, ok := formatAutoCompactionEvent(agent.SessionEvent{
		Type:               agent.SEAutoCompactionEnd,
		CompactionReason:   "threshold",
		CompactionKind:     agent.CompactionKindTrim,
		CompactionStrategy: "light_trim",
		CompactionChanged:  true,
		TokensBefore:       128000,
		TokensAfter:        64000,
	})
	if !ok {
		t.Fatal("expected auto compaction end event to be formatted")
	}
	if muted {
		t.Fatal("expected changed compaction result to use emphasized tone")
	}
	for _, want := range []string{"Context trimmed automatically", "light trim", "128.0k", "64.0k"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestFormatRetryEvent(t *testing.T) {
	t.Parallel()

	text, ok := formatRetryEvent(agent.SessionEvent{
		Type:         agent.SEAutoRetryStart,
		RetryAttempt: 2,
		RetryMax:     3,
		RetryDelay:   3500 * time.Millisecond,
	})
	if !ok {
		t.Fatal("expected retry start event to be formatted")
	}
	for _, want := range []string{"retrying", "2/3", "3.5s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestFormatRetryEventIgnoresOtherEvents(t *testing.T) {
	t.Parallel()

	if _, ok := formatRetryEvent(agent.SessionEvent{
		Type: agent.SEAutoRetryEnd,
	}); ok {
		t.Fatal("expected retry end event to be ignored")
	}

	if _, ok := formatRetryEvent(agent.SessionEvent{
		Type: agent.SEAutoCompactionStart,
	}); ok {
		t.Fatal("expected compaction event to be ignored by retry formatter")
	}
}

func TestFormatRecentToolCalls(t *testing.T) {
	t.Parallel()

	lines := formatRecentToolCalls([]agent.ToolCallSnapshot{
		{
			Tool:      "read",
			ArgsHash:  "abc12345",
			Success:   true,
			Timestamp: time.Date(2026, 3, 27, 15, 4, 5, 0, time.Local),
		},
		{
			Tool:      "grep",
			ArgsHash:  "def67890",
			Success:   false,
			Timestamp: time.Date(2026, 3, 27, 15, 4, 6, 0, time.Local),
		},
	})

	if len(lines) != 2 {
		t.Fatalf("expected two lines, got %d", len(lines))
	}
	for _, want := range []string{"15:04:05", "read", "ok", "abc12345"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("expected %q in %q", want, lines[0])
		}
	}
	for _, want := range []string{"15:04:06", "grep", "error", "def67890"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("expected %q in %q", want, lines[1])
		}
	}
}

func TestFormatErrorCounts(t *testing.T) {
	t.Parallel()

	text := formatErrorCounts(map[apperr.Kind]int{
		apperr.KindCanceled: 2,
		apperr.KindProvider: 1,
		apperr.KindUnknown:  3,
	})

	for _, want := range []string{"canceled=2", "provider=1", "unknown=3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestFormatRecentErrors(t *testing.T) {
	t.Parallel()

	lines := formatRecentErrors([]agent.ErrorSnapshot{
		{
			Kind:      apperr.KindProvider,
			Message:   "provider unavailable",
			Detail:    "dial tcp timeout",
			Timestamp: time.Date(2026, 3, 27, 15, 4, 5, 0, time.Local),
		},
		{
			Kind:      apperr.KindUnknown,
			Message:   "plain failure",
			Timestamp: time.Date(2026, 3, 27, 15, 4, 6, 0, time.Local),
		},
	})

	if len(lines) != 2 {
		t.Fatalf("expected two lines, got %d", len(lines))
	}
	for _, want := range []string{"15:04:05", "provider", "provider unavailable", "dial tcp timeout"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("expected %q in %q", want, lines[0])
		}
	}
	for _, want := range []string{"15:04:06", "unknown", "plain failure"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("expected %q in %q", want, lines[1])
		}
	}
}

func TestFormatLastReminder(t *testing.T) {
	t.Parallel()

	text := formatLastReminder(agent.ReminderSnapshot{
		Kind:      agent.ReminderRepeatToolCall,
		Mode:      "steer",
		Timestamp: time.Date(2026, 3, 27, 15, 4, 7, 0, time.Local),
	}, true)

	for _, want := range []string{"repeat_tool_call", "steer", "15:04:07"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}

	if got := formatLastReminder(agent.ReminderSnapshot{}, false); got != "(none)" {
		t.Fatalf("expected empty reminder placeholder, got %q", got)
	}
}

func TestFormatLastCompaction(t *testing.T) {
	t.Parallel()

	changed := formatLastCompaction(agent.CompactionSnapshot{
		Kind:           agent.CompactionKindTrim,
		Strategy:       "light_trim",
		Reason:         "threshold",
		Changed:        true,
		TokensBefore:   128000,
		TokensAfter:    64000,
		CompactedCount: 12,
		KeptCount:      3,
		SplitTurn:      true,
		Timestamp:      time.Date(2026, 3, 27, 15, 4, 8, 0, time.Local),
	}, true)
	for _, want := range []string{"trim", "threshold", "light trim", "compacted=12", "kept=3", "split-turn", "changed", "128.0k", "64.0k", "15:04:08"} {
		if !strings.Contains(changed, want) {
			t.Fatalf("expected %q in %q", want, changed)
		}
	}

	noop := formatLastCompaction(agent.CompactionSnapshot{
		Kind:      agent.CompactionKindMicro,
		Reason:    "threshold",
		Changed:   false,
		Timestamp: time.Date(2026, 3, 27, 15, 4, 9, 0, time.Local),
	}, true)
	for _, want := range []string{"micro", "threshold", "no-op", "15:04:09"} {
		if !strings.Contains(noop, want) {
			t.Fatalf("expected %q in %q", want, noop)
		}
	}

	if got := formatLastCompaction(agent.CompactionSnapshot{}, false); got != "(none)" {
		t.Fatalf("expected empty compaction placeholder, got %q", got)
	}
}

func TestFormatContextSnapshot(t *testing.T) {
	t.Parallel()

	text := formatContextSnapshot(
		&agentcore.ContextSnapshot{
			Usage: &agentcore.ContextUsage{
				Tokens:         64000,
				ContextWindow:  128000,
				Percent:        50,
				UsageTokens:    32000,
				TrailingTokens: 4000,
			},
			Scope:              "projected",
			TranscriptMessages: 18,
			ActiveMessages:     12,
			SummaryMessages:    1,
			ToolMessages:       4,
			ClearedToolResults: 2,
			TrimmedTextBlocks:  3,
			LastStrategy:       "full_summary",
			LastChanged:        true,
			LastCompactedCount: 9,
			LastKeptCount:      3,
			LastSplitTurn:      true,
		},
		true,
		nil,
		agent.ContextBreakdown{
			UserText:      8000,
			AssistantText: 12000,
			ToolCalls:     2000,
			ToolResults:   40000,
			Total:         62000,
			ContextWindow: 128000,
			TopTools: []agent.ToolTokenUsage{
				{Name: "read", CallTokens: 500, ResultTokens: 20000, Total: 20500},
				{Name: "bash", CallTokens: 1500, ResultTokens: 18000, Total: 19500},
			},
		},
		nil,
		agent.RuntimeMetricsSnapshot{
			CompactionTotal:   4,
			CompactionChanged: 3,
			CompactionSaved:   32000,
			CompactionByKind: map[agent.CompactionKind]int{
				agent.CompactionKindMicro: 1,
				agent.CompactionKindFull:  2,
			},
		},
		agent.CompactionSnapshot{
			Kind:         agent.CompactionKindFull,
			Strategy:     "full_summary",
			Reason:       "manual",
			Changed:      true,
			TokensBefore: 128000,
			TokensAfter:  64000,
			Timestamp:    time.Date(2026, 3, 27, 15, 4, 10, 0, time.Local),
		},
		true,
	)

	for _, want := range []string{
		"Context Snapshot",
		"64.0k (50.0%)",
		"usage=32.0k, trailing=4.0k",
		"Token breakdown",
		"User text",
		"Tool results",
		"Top tools",
		"read",
		"bash",
		"projected view",
		"12 active / 18 transcript",
		"Summary checkpoints",
		"Cleared results",
		"full summary",
		"compacted=9, kept=3, split-turn",
		"full / manual / full summary",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestFormatRunSummary(t *testing.T) {
	t.Parallel()

	text := formatRunSummary(agentcore.RunSummary{
		TurnCount:  3,
		ToolCalls:  5,
		ToolErrors: 1,
		EndReason:  agentcore.EndReasonMaxTurns,
	}, true)
	for _, want := range []string{"max_turns", "turns=3", "tool_calls=5", "tool_errors=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}

	if got := formatRunSummary(agentcore.RunSummary{}, false); got != "(none)" {
		t.Fatalf("expected empty run summary placeholder, got %q", got)
	}
}

func TestFormatReminderCounts(t *testing.T) {
	t.Parallel()

	got := formatReminderCounts(map[agent.RuntimeReminderKind]int{
		agent.ReminderRepeatToolCall:     1,
		agent.ReminderPostStopValidation: 2,
	})
	want := "repeat_tool_call=1, post_stop_validation=2"
	if got != want {
		t.Fatalf("formatReminderCounts() = %q, want %q", got, want)
	}
}

func TestFormatCompactionSavings(t *testing.T) {
	t.Parallel()

	got := formatCompactionSavings(map[agent.CompactionKind]int{
		agent.CompactionKindTrim: 1200,
		agent.CompactionKindFull: 32000,
	})
	want := "trim=1.2k, full=32.0k"
	if got != want {
		t.Fatalf("formatCompactionSavings() = %q, want %q", got, want)
	}
}
