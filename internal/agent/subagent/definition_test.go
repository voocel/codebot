package subagent

import (
	"slices"
	"strings"
	"testing"
)

// An override keeps the built-in's position; new names go last.
func TestMergeAgents_LaterSourceWins(t *testing.T) {
	builtin := []AgentDefinition{
		{Name: "explore", Description: "builtin explore"},
		{Name: "plan", Description: "builtin plan"},
	}
	loaded := []AgentDefinition{
		{Name: "explore", Description: "loaded explore"},
		{Name: "personal", Description: "loaded-only agent"},
	}

	var got []string
	for _, d := range mergeAgents(builtin, loaded) {
		got = append(got, d.Name+":"+d.Description)
	}
	want := []string{"explore:loaded explore", "plan:builtin plan", "personal:loaded-only agent"}
	if !slices.Equal(got, want) {
		t.Errorf("merged = %q, want %q", got, want)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	cases := []struct {
		def  AgentDefinition
		want string
	}{
		{AgentDefinition{Name: "a", SystemPrompt: "y"}, "missing description"},
		{AgentDefinition{Name: "a", Description: "x", SystemPrompt: "y", Tools: []string{"read", ""}}, "empty entry in tools"},
		{AgentDefinition{Name: "a", Description: "x", SystemPrompt: "y", DisallowedTools: []string{""}}, "empty entry in disallowedTools"},
	}
	for _, c := range cases {
		if err := c.def.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%+v) = %v, want an error containing %q", c.def, err, c.want)
		}
	}
}
