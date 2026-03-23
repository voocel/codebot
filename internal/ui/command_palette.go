package ui

import (
	"sort"
	"strings"

	"github.com/voocel/codebot/internal/ui/tui"
)

type commandPaletteMatch struct {
	item  tui.CompletionItem
	score int
}

func (a *App) completions(prefix string) []tui.CompletionItem {
	query := strings.TrimSpace(strings.ToLower(prefix))
	var matches []commandPaletteMatch

	for _, cmd := range a.registry.All() {
		spec := a.registry.EffectiveSpec(cmd)
		if spec.Hidden {
			continue
		}
		item, score, ok := buildCommandPaletteItem(spec, query)
		if !ok {
			continue
		}
		matches = append(matches, commandPaletteMatch{item: item, score: score})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		kindI := commandKindOrder[CommandKind(matches[i].item.Kind)]
		kindJ := commandKindOrder[CommandKind(matches[j].item.Kind)]
		if kindI != kindJ {
			return kindI < kindJ
		}
		return strings.Compare(matches[i].item.Name, matches[j].item.Name) < 0
	})

	items := make([]tui.CompletionItem, 0, len(matches))
	for _, match := range matches {
		items = append(items, match.item)
	}
	return items
}

func buildCommandPaletteItem(spec CommandSpec, query string) (tui.CompletionItem, int, bool) {
	item := tui.CompletionItem{
		Name:        spec.Name,
		Description: spec.Description,
		Usage:       spec.Usage,
		Kind:        string(spec.Kind),
		Category:    spec.Category,
		NeedsIdle:   spec.NeedsIdle,
		Source:      spec.Source,
		Aliases:     append([]string(nil), spec.Aliases...),
		AutoExecute: commandPaletteAutoExecute(spec),
		Placeholder: spec.Placeholder,
	}

	if item.Description == "" {
		item.Description = "(no description)"
	}
	if item.Usage == "" {
		item.Usage = "/" + item.Name
	}
	if item.Category == "" {
		item.Category = "prompt"
	}

	score, ok := scoreCommandPaletteItem(spec, query)
	return item, score, ok
}

func scoreCommandPaletteItem(spec CommandSpec, query string) (int, bool) {
	if query == "" {
		return 100, true
	}

	name := strings.ToLower(spec.Name)

	bestScore := 0
	if score := scorePaletteField(name, query, 1200, 950, 700); score > bestScore {
		bestScore = score
	}
	for _, alias := range spec.Aliases {
		if score := scorePaletteField(strings.ToLower(alias), query, 1100, 900, 650); score > bestScore {
			bestScore = score
		}
	}

	return bestScore, bestScore > 0
}

func scorePaletteField(field, query string, exact, prefix, contains int) int {
	if field == "" {
		return 0
	}
	switch {
	case field == query:
		return exact
	case strings.HasPrefix(field, query):
		return prefix - min(len(field)-len(query), 40)
	case strings.Contains(field, query):
		return contains
	default:
		return 0
	}
}

func commandPaletteAutoExecute(spec CommandSpec) bool {
	usage := strings.TrimSpace(spec.Usage)
	if usage == "" {
		return true
	}
	return usage == "/"+spec.Name
}
