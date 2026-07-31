package skill

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type ListingOptions struct {
	CharBudget       int
	MaxLineChars     int
	MaxWhenChars     int
	IncludeWhenToUse bool
}

func DefaultListingOptions() ListingOptions {
	return ListingOptions{
		CharBudget:       4000,
		MaxLineChars:     220,
		MaxWhenChars:     160,
		IncludeWhenToUse: true,
	}
}

// OrderForPrompt ranks skills by relevance (applicable → usage → source →
// name). The ranking decides which skills SURVIVE RenderListing's char budget,
// not the order they appear in — see RenderListing.
func OrderForPrompt(skills []Spec, cwd string, usage map[string]float64) []Spec {
	ordered := append([]Spec(nil), skills...)
	sortSkillsForPrompt(ordered, cwd, usage)
	return ordered
}

const listingHeader = "The following skills are available for use with the Skill tool:\n\n"

// RenderListing renders the skill listing that goes into the cached system
// block. Two-phase on purpose:
//
//  1. walk `skills` in the caller's order (OrderForPrompt's usage ranking) and
//     keep entries until the char budget runs out — the most relevant skills
//     win the budget;
//  2. sort the survivors by a time-independent key and render.
//
// Phase 2 is what makes the output byte-stable. Usage scores decay with wall
// time, so rendering in ranking order would reshuffle the block on every
// rebuild and invalidate the prompt cache prefix for no semantic gain. The
// output only changes when the surviving SET changes, which needs the catalog
// to exceed the budget AND a ranking swap across the cutoff.
func RenderListing(skills []Spec, opts ListingOptions) string {
	if len(skills) == 0 {
		return ""
	}
	if opts.CharBudget <= 0 {
		opts.CharBudget = DefaultListingOptions().CharBudget
	}
	if opts.MaxLineChars <= 0 {
		opts.MaxLineChars = DefaultListingOptions().MaxLineChars
	}
	if opts.MaxWhenChars <= 0 {
		opts.MaxWhenChars = DefaultListingOptions().MaxWhenChars
	}

	type entry struct {
		spec Spec
		text string
	}

	// Phase 1 — budget selection in relevance order.
	var selected []entry
	used := len(listingHeader)
	for _, spec := range skills {
		if spec.DisableModelInvocation {
			continue
		}
		line := "- " + spec.Name
		if spec.ArgumentHint != "" {
			line += " " + spec.ArgumentHint
		}
		if desc := strings.TrimSpace(spec.Description); desc != "" {
			line += ": " + desc
		}
		text := truncate(line, opts.MaxLineChars) + "\n"
		if used+len(text)+1 > opts.CharBudget {
			break
		}
		used += len(text)
		if opts.IncludeWhenToUse {
			if when := strings.TrimSpace(spec.WhenToUse); when != "" {
				whenLine := "  when: " + truncate(when, opts.MaxWhenChars) + "\n"
				if used+len(whenLine) <= opts.CharBudget {
					text += whenLine
					used += len(whenLine)
				}
			}
		}
		selected = append(selected, entry{spec: spec, text: text})
	}
	if len(selected) == 0 {
		return ""
	}

	// Phase 2 — stable presentation order, independent of usage/time.
	sort.SliceStable(selected, func(i, j int) bool {
		return lessStable(selected[i].spec, selected[j].spec)
	})

	var sb strings.Builder
	sb.Grow(used + 128)
	sb.WriteString(listingHeader)
	for _, item := range selected {
		sb.WriteString(item.text)
	}
	sb.WriteString("\nIMPORTANT: Only use Skill for skills listed above - do not guess or use built-in CLI commands.")
	return sb.String()
}

// lessStable orders skills by source priority then name — no wall-clock or
// usage input, so equal inputs always produce equal bytes.
func lessStable(a, b Spec) bool {
	aSource, bSource := sourcePriority(a.Source), sourcePriority(b.Source)
	if aSource != bSource {
		return aSource < bSource
	}
	return a.Name < b.Name
}

func sortSkillsForPrompt(skills []Spec, cwd string, usage map[string]float64) {
	if len(skills) < 2 {
		return
	}
	sort.SliceStable(skills, func(i, j int) bool {
		a, b := skills[i], skills[j]
		aApplicable := skillIsActive(a, cwd)
		bApplicable := skillIsActive(b, cwd)
		if aApplicable != bApplicable {
			return aApplicable
		}
		aUsage := usage[NormalizeName(a.Name)]
		bUsage := usage[NormalizeName(b.Name)]
		if aUsage != bUsage {
			return aUsage > bUsage
		}
		aSource := sourcePriority(a.Source)
		bSource := sourcePriority(b.Source)
		if aSource != bSource {
			return aSource < bSource
		}
		return a.Name < b.Name
	})
}

func skillIsActive(spec Spec, cwd string) bool {
	if len(spec.Paths) == 0 || cwd == "" {
		return true
	}
	for _, pattern := range spec.Paths {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pathPatternExists(cwd, pattern) {
			return true
		}
	}
	return false
}

func pathPatternExists(cwd, pattern string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "**") {
		matches, err := filepath.Glob(filepath.Join(cwd, filepath.FromSlash(pattern)))
		return err == nil && len(matches) > 0
	}

	root, matcher, err := compileDoubleStarPattern(pattern)
	if err != nil {
		return false
	}
	rootPath := filepath.Join(cwd, root)
	if _, err := os.Stat(rootPath); err != nil {
		return false
	}

	found := false
	_ = filepath.WalkDir(rootPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if matcher.MatchString(slashRel) {
			found = true
			return filepath.SkipAll
		}
		if d.IsDir() && matcher.MatchString(slashRel+"/") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func compileDoubleStarPattern(pattern string) (string, *regexp.Regexp, error) {
	parts := strings.Split(pattern, "/")
	rootParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.Contains(part, "**") || strings.ContainsAny(part, "*?") {
			break
		}
		if part != "" {
			rootParts = append(rootParts, part)
		}
	}
	root := "."
	if len(rootParts) > 0 {
		root = filepath.Join(rootParts...)
	}
	re, err := regexp.Compile(globToRegexp(pattern))
	if err != nil {
		return "", nil, err
	}
	return root, re, nil
}

func globToRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i += 2
				continue
			}
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
		i++
	}
	b.WriteString("$")
	return b.String()
}

func sourcePriority(source string) int {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "project":
		return 0
	case "user":
		return 1
	case "bundled":
		return 2
	default:
		return 3
	}
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return string(runes[:1])
	}
	return string(runes[:max-1]) + "…"
}
