package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrderForPromptPrefersApplicableAndUsedSkills(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "internal", "skill"), 0o755); err != nil {
		t.Fatal(err)
	}

	skills := []Spec{
		{Name: "unused", Source: "bundled"},
		{Name: "review", Source: "bundled", Paths: []string{"internal/skill/**"}},
		{Name: "debug", Source: "project"},
	}
	ordered := OrderForPrompt(skills, cwd, map[string]int{
		"review": 3,
		"debug":  1,
	})

	if ordered[0].Name != "review" {
		t.Fatalf("expected applicable + most-used skill first, got %#v", ordered)
	}
	if ordered[1].Name != "debug" {
		t.Fatalf("expected next used/project skill second, got %#v", ordered)
	}
}

func TestRenderListingTruncatesWhenToUseIndependently(t *testing.T) {
	t.Parallel()

	when := strings.Repeat("a", 240)
	out := RenderListing([]Spec{
		{
			Name:        "review",
			Description: "review code carefully",
			WhenToUse:   when,
		},
	}, ListingOptions{
		CharBudget:       4000,
		MaxLineChars:     220,
		MaxWhenChars:     80,
		IncludeWhenToUse: true,
	})

	if !strings.Contains(out, "- review: review code carefully") {
		t.Fatalf("expected skill line in listing, got %q", out)
	}
	if !strings.Contains(out, "  when: ") {
		t.Fatalf("expected when_to_use line in listing, got %q", out)
	}
	if strings.Contains(out, "  when: "+when) {
		t.Fatalf("expected when_to_use to be truncated, got %q", out)
	}
	if !strings.Contains(out, "  when: "+strings.Repeat("a", 79)+"…") {
		t.Fatalf("expected independent when_to_use truncation, got %q", out)
	}
}
