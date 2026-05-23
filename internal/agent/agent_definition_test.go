package agent

import (
	"strings"
	"testing"
)

// MergeAgents resolves name collisions by the order groups were passed. The
// expected ordering is builtin → project → user, so a user file overrides a
// project file overrides a built-in. Verify the override actually wins.
func TestMergeAgents_LaterSourceWins(t *testing.T) {
	builtin := []AgentDefinition{
		{Name: "explore", Description: "builtin explore", Source: SourceBuiltin},
		{Name: "plan", Description: "builtin plan", Source: SourceBuiltin},
	}
	project := []AgentDefinition{
		{Name: "explore", Description: "project explore", Source: SourceProject},
	}
	user := []AgentDefinition{
		{Name: "plan", Description: "user plan", Source: SourceUser},
		{Name: "personal", Description: "user-only agent", Source: SourceUser},
	}

	merged := MergeAgents(builtin, project, user)

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

	// Source overrides actually replaced the content, not just the metadata.
	if merged[0].Description != "project explore" {
		t.Errorf("explore should be project override, got %q", merged[0].Description)
	}
	if merged[0].Source != SourceProject {
		t.Errorf("explore source should be project, got %q", merged[0].Source)
	}
	if merged[1].Description != "user plan" {
		t.Errorf("plan should be user override, got %q", merged[1].Description)
	}
	if merged[1].Source != SourceUser {
		t.Errorf("plan source should be user, got %q", merged[1].Source)
	}
}

// Empty-name entries are silently skipped during merge. The Validate step
// upstream is the right place to surface schema errors; MergeAgents must
// be tolerant so a half-broken file group doesn't kill the rest.
func TestMergeAgents_SkipsEmptyNames(t *testing.T) {
	groups := [][]AgentDefinition{
		{{Name: "a", Description: "a"}},
		{{Name: "", Description: "anonymous"}, {Name: "b", Description: "b"}},
	}
	merged := MergeAgents(groups...)
	if len(merged) != 2 {
		t.Fatalf("expected 2, got %d", len(merged))
	}
	if merged[0].Name != "a" || merged[1].Name != "b" {
		t.Errorf("unexpected merged set: %+v", merged)
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

// IsBuiltIn maps the source enum to the trust tier used by FilterToolsForAgent.
// Pin the mapping so any future addition of a new source forces a decision
// (and a corresponding test update).
func TestAgentSource_IsBuiltIn(t *testing.T) {
	cases := map[AgentSource]bool{
		SourceBuiltin: true,
		SourceProject: false,
		SourceUser:    false,
	}
	for src, want := range cases {
		if got := src.IsBuiltIn(); got != want {
			t.Errorf("%s.IsBuiltIn() = %v, want %v", src, got, want)
		}
	}
}
