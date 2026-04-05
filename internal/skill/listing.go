package skill

import (
	"fmt"
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

func OrderForPrompt(skills []Spec, cwd string, usage map[string]float64) []Spec {
	ordered := append([]Spec(nil), skills...)
	sortSkillsForPrompt(ordered, cwd, usage)
	return ordered
}

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

	var sb strings.Builder
	sb.WriteString("The following skills are available for use with the Skill tool:\n\n")
	for _, spec := range skills {
		if spec.DisableModelInvocation {
			continue
		}
		line := fmt.Sprintf("- %s", spec.Name)
		if spec.ArgumentHint != "" {
			line += " " + spec.ArgumentHint
		}
		desc := strings.TrimSpace(spec.Description)
		if desc != "" {
			line += ": " + desc
		}
		line = truncate(line, opts.MaxLineChars)
		if sb.Len()+len(line)+2 > opts.CharBudget {
			break
		}
		sb.WriteString(line)
		sb.WriteString("\n")
		if opts.IncludeWhenToUse {
			when := strings.TrimSpace(spec.WhenToUse)
			if when != "" {
				whenLine := "  when: " + truncate(when, opts.MaxWhenChars)
				if sb.Len()+len(whenLine)+1 <= opts.CharBudget {
					sb.WriteString(whenLine)
					sb.WriteString("\n")
				}
			}
		}
	}
	if sb.Len() == len("The following skills are available for use with the Skill tool:\n\n") {
		return ""
	}
	sb.WriteString("\nIMPORTANT: Only use Skill for skills listed above - do not guess or use built-in CLI commands.")
	return sb.String()
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
