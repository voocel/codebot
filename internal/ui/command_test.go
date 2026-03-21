package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
)

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

	engine, err := approval.NewEngine(t.TempDir(), "balanced", nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetMode(approval.ModePlan)

	spec := CommandSpec{
		Name:      "new",
		Category:  "session",
		NeedsIdle: false,
	}
	if err := validateCommand(context.Background(), engine, spec, false); err == nil {
		t.Fatalf("expected plan mode to deny session command")
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
		Skills: []config.Skill{
			{Name: "review", Description: "Code review skill"},
		},
	}

	app.rebuildRegistry()

	for _, name := range []string{"help", "deploy", "skill:review"} {
		if _, ok := app.registry.Lookup(name); !ok {
			t.Fatalf("expected command %q to be registered", name)
		}
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

	descItems := app.completions("staging")
	if len(descItems) == 0 || descItems[0].Name != "deploy" {
		t.Fatalf("expected description query to resolve deploy, got %#v", descItems)
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
		Type:              agent.SEAutoCompactionEnd,
		CompactionReason:  "threshold",
		CompactionChanged: true,
		TokensBefore:      128000,
		TokensAfter:       64000,
	})
	if !ok {
		t.Fatal("expected auto compaction end event to be formatted")
	}
	if muted {
		t.Fatal("expected changed compaction result to use emphasized tone")
	}
	for _, want := range []string{"Context compacted automatically", "128.0k", "64.0k"} {
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
