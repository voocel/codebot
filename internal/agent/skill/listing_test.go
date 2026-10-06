package skill

import (
	"fmt"
	"strings"
	"testing"
)

func TestListing(t *testing.T) {
	t.Parallel()

	result := Listing([]Spec{
		{Name: "commit", Description: "Git commit helper", WhenToUse: "after changes"},
		{Name: "hidden", Description: "Hidden skill", DisableModelInvocation: true},
		{Name: "review", Description: "Code reviewer", ArgumentHint: "<pr>"},
		{Name: "conventions", Description: "API conventions", DisableUserInvocation: true},
	}, nil)

	for _, want := range []string{
		"- commit: Git commit helper\n  when: after changes\n",
		"- review <pr>: Code reviewer\n",
		"- conventions: API conventions\n",
		"Skill tool",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("listing missing %q:\n%s", want, result)
		}
	}
	if strings.Contains(result, "hidden") {
		t.Error("a skill the model may not invoke must not be listed")
	}
	if result := Listing(nil, nil); result != "" {
		t.Errorf("no skills to list must list nothing, got %q", result)
	}
}

// The listing sits in the cached prompt prefix: usage scores, which decay
// with time, may decide which skills fit, never the bytes of the ones that do.
func TestListingIsStableAsUsageChanges(t *testing.T) {
	t.Parallel()

	skills := []Spec{
		{Name: "zeta", Description: "Z", Source: "project"},
		{Name: "alpha", Description: "A", Source: "bundled"},
		{Name: "mid", Description: "M", Source: "user"},
	}
	first := Listing(skills, map[string]float64{"zeta": 3})
	second := Listing([]Spec{skills[1], skills[2], skills[0]}, map[string]float64{"alpha": 5, "mid": 1})
	if first != second {
		t.Fatalf("listing moved with usage:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if !strings.Contains(first, "- alpha: A\n- mid: M\n- zeta: Z\n") {
		t.Fatalf("expected the skills by name:\n%s", first)
	}
}

// When the skills overflow the budget, the most used ones are kept.
func TestListingKeepsTheMostUsedOverBudget(t *testing.T) {
	t.Parallel()

	var skills []Spec
	for i := range 40 {
		skills = append(skills, Spec{Name: fmt.Sprintf("skill-%02d", i), Description: strings.Repeat("x", 150)})
	}
	result := Listing(skills, map[string]float64{"skill-39": 2})
	if !strings.Contains(result, "- skill-39:") {
		t.Fatal("the most used skill must make the budget")
	}
	if strings.Contains(result, "- skill-38:") {
		t.Fatal("the budget must cut the least used skills")
	}
}
