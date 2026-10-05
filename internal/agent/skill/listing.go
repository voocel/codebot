package skill

import (
	"cmp"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	listingBudget = 4000 // characters
	maxLineChars  = 220
	maxWhenChars  = 160
)

const listingHeader = "The following skills are available for use with the Skill tool:\n\n"

// Listing renders the skills the model may invoke for the system prompt.
// The ones used most, by usage score, then those from the most trusted
// sources, win the budget. The listing itself is ordered by source and name,
// so it changes only when the skills in it do, not as usage scores decay: it
// sits in the cached prefix of every request.
func Listing(skills []Spec, usage map[string]float64) string {
	ranked := slices.Clone(skills)
	slices.SortStableFunc(ranked, func(a, b Spec) int {
		if ua, ub := usage[a.Name], usage[b.Name]; ua != ub {
			return cmp.Compare(ub, ua)
		}
		return compareStable(a, b)
	})

	type entry struct {
		spec Spec
		text string
	}
	var selected []entry
	used := len(listingHeader)
	for _, spec := range ranked {
		if spec.DisableModelInvocation {
			continue
		}
		line := "- " + spec.Name
		if spec.ArgumentHint != "" {
			line += " " + spec.ArgumentHint
		}
		if spec.Description != "" {
			line += ": " + spec.Description
		}
		text := truncate(line, maxLineChars) + "\n"
		if used+len(text)+1 > listingBudget {
			break
		}
		used += len(text)
		if spec.WhenToUse != "" {
			when := "  when: " + truncate(spec.WhenToUse, maxWhenChars) + "\n"
			if used+len(when) <= listingBudget {
				text += when
				used += len(when)
			}
		}
		selected = append(selected, entry{spec, text})
	}
	if len(selected) == 0 {
		return ""
	}
	slices.SortFunc(selected, func(a, b entry) int { return compareStable(a.spec, b.spec) })

	var sb strings.Builder
	sb.WriteString(listingHeader)
	for _, item := range selected {
		sb.WriteString(item.text)
	}
	sb.WriteString("\nOnly the skills listed above exist; the Skill tool cannot run CLI commands.")
	return sb.String()
}

// compareStable orders skills by source, then name: nothing that changes
// with time.
func compareStable(a, b Spec) int {
	return cmp.Or(cmp.Compare(sourcePriority(a.Source), sourcePriority(b.Source)), strings.Compare(a.Name, b.Name))
}

func skillIsActive(spec Spec, cwd string) bool {
	if len(spec.Paths) == 0 || cwd == "" {
		return true
	}
	return slices.ContainsFunc(spec.Paths, func(pattern string) bool { return pathPatternExists(cwd, pattern) })
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
	switch source {
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
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
