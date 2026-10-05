package markdown

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// span is text in one style.
type span struct {
	text string
	st   lipgloss.Style
}

func renderSpans(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.st.Render(s.text))
	}
	return b.String()
}

func spansText(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}
	return b.String()
}

func spansWidth(spans []span) int { return ansi.StringWidth(spansText(spans)) }

// Wrap lays plain text out in lines at most width wide, in st, keeping its
// line breaks and spacing.
func Wrap(s string, st lipgloss.Style, width int) []string {
	width = max(width, 1)
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\t", "    "), "\n") {
		if ansi.StringWidth(line) > width {
			line = ansi.Wrap(line, width, "")
		}
		for _, part := range strings.Split(line, "\n") {
			out = append(out, st.Render(part))
		}
	}
	return out
}

// layout lays spans out in lines at most width wide, breaking between words
// and inside a word longer than a line. Runs of spaces read as one, as
// markdown has them.
func layout(spans []span, width int) []string {
	l := liner{width: width}
	for _, s := range spans {
		text := strings.ReplaceAll(s.text, "\t", "    ")
		for text != "" {
			i := strings.IndexAny(text, " \n")
			if i < 0 {
				l.add(text, s.st)
				break
			}
			if i > 0 {
				l.add(text[:i], s.st)
			}
			l.endWord()
			if text[i] == '\n' {
				l.newline()
			} else if l.width > 0 && l.lineW > 0 {
				l.space = &s.st
			}
			text = text[i+1:]
		}
	}
	l.endWord()
	if l.lineW > 0 || len(l.lines) == 0 {
		l.newline()
	}
	return l.lines
}

type liner struct {
	width int
	lines []string

	line  strings.Builder
	lineW int

	word  []span // the word being read, in pieces of one style
	wordW int
	// space is the style of a space waiting to go between the line's last
	// word and the next; nil when none waits.
	space *lipgloss.Style
}

func (l *liner) add(text string, st lipgloss.Style) {
	l.word = append(l.word, span{text, st})
	l.wordW += ansi.StringWidth(text)
}

func (l *liner) newline() {
	l.lines = append(l.lines, l.line.String())
	l.line.Reset()
	l.lineW = 0
	l.space = nil
}

// endWord puts the word read on the line, on the next one if it does not fit.
func (l *liner) endWord() {
	defer func() { l.word, l.wordW = nil, 0 }()
	if l.wordW == 0 {
		return
	}
	sp := 0
	if l.space != nil {
		sp = 1
	}
	if l.lineW > 0 && l.lineW+sp+l.wordW > l.width {
		l.newline()
	}
	if l.space != nil {
		l.line.WriteString(l.space.Render(" "))
		l.lineW++
		l.space = nil
	}
	for _, p := range l.word {
		text := p.text
		for text != "" {
			room := l.width - l.lineW
			if w := ansi.StringWidth(text); w <= room {
				l.line.WriteString(p.st.Render(text))
				l.lineW += w
				break
			}
			head := ansi.Truncate(text, room, "")
			if head == "" {
				if l.lineW > 0 {
					l.newline()
					continue
				}
				// A character wider than the whole line goes on its own.
				_, size := utf8.DecodeRuneInString(text)
				head = text[:size]
			}
			l.line.WriteString(p.st.Render(head))
			text = text[len(head):]
			l.newline()
		}
	}
}
