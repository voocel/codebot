package subagent

import (
	"strings"
	"testing"
)

// mergeAgents resolves name collisions by the order groups were passed. The
// expected ordering is builtin → project → user, so a user file overrides a
// project file overrides a built-in. Verify the override actually wins.
func TestMergeAgents_LaterSourceWins(t *testing.T) {
	builtin := []AgentDefinition{
		{Name: "explore", Description: "builtin explore"},
		{Name: "plan", Description: "builtin plan"},
	}
	project := []AgentDefinition{
		{Name: "explore", Description: "project explore"},
	}
	user := []AgentDefinition{
		{Name: "plan", Description: "user plan"},
		{Name: "personal", Description: "user-only agent"},
	}

	merged := mergeAgents(builtin, project, user)

	// Three distinct names, in insertion order (explore from builtin slot,
	// plan from builtin slot, personal added at the end).
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged agents, got %d", len(merged))
	}
	if merged[0].Name != "explore" || merged[1].Name != "plan" || merged[2].Name != "personal" {
		var names []string
		for _, d := range merged {
			names = append(names, d.Name)
		}
		t.Errorf("merge order = %v, want [explore plan personal]", names)
	}

	// The later definitions replaced the content.
	if merged[0].Description != "project explore" {
		t.Errorf("explore should be project override, got %q", merged[0].Description)
	}
	if merged[1].Description != "user plan" {
		t.Errorf("plan should be user override, got %q", merged[1].Description)
	}
}

func TestBuiltinDefinitionsUseGeneralPurposeName(t *testing.T) {
	defs := builtinDefinitions("/tmp/ws")
	var names []string
	for _, def := range defs {
		names = append(names, def.Name)
	}
	want := []string{"explore", "plan", "general-purpose"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("builtin names = %v, want %v", names, want)
	}
}

// Validate must fail when any required field is empty. Tests are tabular
// because the four checks are nearly identical and a table makes adding a
// fifth check trivial.
func TestValidate_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		def  AgentDefinition
		want string
	}{
		{"missing name", AgentDefinition{Description: "x", SystemPrompt: "y"}, "missing name"},
		{"missing description", AgentDefinition{Name: "a", SystemPrompt: "y"}, "missing description"},
		{"missing prompt", AgentDefinition{Name: "a", Description: "x"}, "missing system prompt"},
		{"empty tool entry", AgentDefinition{
			Name: "a", Description: "x", SystemPrompt: "y", Tools: []string{"read", ""},
		}, "empty entry in tools"},
		{"empty disallow entry", AgentDefinition{
			Name: "a", Description: "x", SystemPrompt: "y", DisallowedTools: []string{""},
		}, "empty entry in disallowedTools"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.def.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

// Validate passes a fully-populated definition without complaint. A regression
// here would mean Validate grew an unintended new requirement.
func TestValidate_HappyPath(t *testing.T) {
	def := AgentDefinition{
		Name:         "ok",
		Description:  "ok",
		SystemPrompt: "ok",
	}
	if err := def.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
