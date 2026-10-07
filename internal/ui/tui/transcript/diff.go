package transcript

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/syntax"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// renderDiff renders the edit and write tools' diff, whose lines look like
// "+ 12 code". A line changed in place has its changed part marked.
func renderDiff(diff, path string, width int) []string {
	ls := lines(diff)
	if len(ls) == 0 {
		return []string{theme.SubtleText.Render("No changes")}
	}
	for i, l := range ls {
		ls[i] = strings.ReplaceAll(l, "\t", "    ")
	}

	var added, removed int
	for _, l := range ls {
		switch {
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	var stats []string
	if added > 0 {
		stats = append(stats, fmt.Sprintf("added %d %s", added, plural(added, "line")))
	}
	if removed > 0 {
		stats = append(stats, fmt.Sprintf("removed %d %s", removed, plural(removed, "line")))
	}
	if len(stats) == 0 {
		return []string{theme.SubtleText.Render("No changes")}
	}
	summary := strings.Join(stats, ", ")
	out := []string{theme.MutedText.Render(strings.ToUpper(summary[:1]) + summary[1:])}

	add := diffSide{gutter: lipgloss.NewStyle().Background(theme.DiffAdd).Foreground(theme.Success), body: lipgloss.NewStyle().Background(theme.DiffAdd), word: lipgloss.NewStyle().Background(theme.DiffAddWord)}
	del := diffSide{gutter: lipgloss.NewStyle().Background(theme.DiffRemove).Foreground(theme.Danger), body: lipgloss.NewStyle().Background(theme.DiffRemove), word: lipgloss.NewStyle().Background(theme.DiffRemoveWord)}

	for i := 0; i < len(ls); {
		l := ls[i]
		switch {
		case strings.HasPrefix(l, "-"):
			var dels, adds []string
			for ; i < len(ls) && strings.HasPrefix(ls[i], "-"); i++ {
				dels = append(dels, ls[i])
			}
			for ; i < len(ls) && strings.HasPrefix(ls[i], "+"); i++ {
				adds = append(adds, ls[i])
			}
			if len(dels) == 1 && len(adds) == 1 {
				d, a := changedInPlace(dels[0], adds[0])
				out = append(out, del.line(dels[0], path, width, d)...)
				out = append(out, add.line(adds[0], path, width, a)...)
				continue
			}
			for _, d := range dels {
				out = append(out, del.line(d, path, width, nil)...)
			}
			for _, a := range adds {
				out = append(out, add.line(a, path, width, nil)...)
			}
		case strings.HasPrefix(l, "+"):
			out = append(out, add.line(l, path, width, nil)...)
			i++
		default:
			prefix, code := splitDiffLine(l)
			out = append(out, fit(theme.SubtleText.Render(prefix)+syntax.File(code, path), width))
			i++
		}
	}
	return out
}

type diffSide struct {
	gutter, body, word lipgloss.Style
}

// mark, when set, is the changed part of l, in runes [from, to).
func (s diffSide) line(l, path string, width int, mark *[2]int) []string {
	prefix, code := splitDiffLine(l)
	room := max(width-ansi.StringWidth(prefix), 4)
	cont := prefix[:1] + strings.Repeat(" ", len(prefix)-1)
	var out []string
	offset := 0
	for i, chunk := range chunks(code, room) {
		head := prefix
		if i > 0 {
			head = cont
		}
		pad := s.body.Render(strings.Repeat(" ", room-ansi.StringWidth(chunk)))
		out = append(out, s.gutter.Render(head)+s.paint(chunk, path, mark, offset)+pad)
		offset += utf8.RuneCountInString(chunk)
	}
	return out
}

// paint expects chunk to start offset runes into its line.
func (s diffSide) paint(chunk, path string, mark *[2]int, offset int) string {
	if mark == nil {
		return s.body.Render(syntax.File(chunk, path))
	}
	r := []rune(chunk)
	from := min(max(mark[0]-offset, 0), len(r))
	to := min(max(mark[1]-offset, from), len(r))
	return s.body.Render(syntax.File(string(r[:from]), path)) +
		s.word.Render(syntax.File(string(r[from:to]), path)) +
		s.body.Render(syntax.File(string(r[to:]), path))
}

func chunks(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	for s != "" {
		head := ansi.Truncate(s, width, "")
		if head == "" {
			_, n := utf8.DecodeRuneInString(s)
			head = s[:n]
		}
		out = append(out, head)
		s = s[len(head):]
	}
	return out
}

// splitDiffLine splits "-  5 code" into "-  5 " and "code".
func splitDiffLine(l string) (prefix, code string) {
	i := 1
	for i < len(l) && l[i] == ' ' {
		i++
	}
	for i < len(l) && l[i] >= '0' && l[i] <= '9' {
		i++
	}
	if i < len(l) && l[i] == ' ' {
		i++
	}
	return l[:i], l[i:]
}

// changedInPlace returns each side's rune range between the prefix and
// suffix the two lines share.
func changedInPlace(removed, added string) (*[2]int, *[2]int) {
	_, a := splitDiffLine(removed)
	_, b := splitDiffLine(added)
	ra, rb := []rune(a), []rune(b)
	pre := 0
	for pre < len(ra) && pre < len(rb) && ra[pre] == rb[pre] {
		pre++
	}
	suf := 0
	for suf < len(ra)-pre && suf < len(rb)-pre && ra[len(ra)-1-suf] == rb[len(rb)-1-suf] {
		suf++
	}
	return &[2]int{pre, len(ra) - suf}, &[2]int{pre, len(rb) - suf}
}
