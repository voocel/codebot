package ui

import (
	"slices"
	"testing"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/policy"
)

func TestValidateCommandRequiresIdleWhenRunning(t *testing.T) {
	t.Parallel()

	spec := CommandSpec{
		Risk:      policy.RiskLow,
		NeedsIdle: true,
	}
	if err := validateCommand(policy.ProfileBalanced, spec, true); err == nil {
		t.Fatalf("expected running agent command to be denied")
	}
}

func TestValidateCommandAllowsInfoCommandWhenRunning(t *testing.T) {
	t.Parallel()

	spec := CommandSpec{
		Risk:      policy.RiskLow,
		NeedsIdle: false,
	}
	if err := validateCommand(policy.ProfileBalanced, spec, true); err != nil {
		t.Fatalf("expected info command to pass while running: %v", err)
	}
}

func TestValidateCommandStillAppliesRiskPolicy(t *testing.T) {
	t.Parallel()

	spec := CommandSpec{
		Risk:      policy.RiskMedium,
		NeedsIdle: false,
	}
	if err := validateCommand(policy.ProfileStrict, spec, false); err == nil {
		t.Fatalf("expected strict profile to deny medium risk command")
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
