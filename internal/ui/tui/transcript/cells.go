package transcript

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

type Prompt struct {
	rev
	still
	Text    string
	Images  int
	Kind    PromptKind
	toggled bool
}

type PromptKind int

const (
	ToAgent PromptKind = iota
	ToCommand
	ToShell
)

// promptLines is how many lines of a long prompt show when collapsed.
const promptLines = 12

func (c *Prompt) Toggle() { c.toggled = !c.toggled; c.bump() }

func (c *Prompt) Render(p Params) []string {
	band := lipgloss.NewStyle().Background(theme.Surface)
	text := band.Foreground(theme.Fg)
	mark, markStyle := "❯", band.Foreground(theme.Accent).Bold(true)
	switch c.Kind {
	case ToCommand:
		markStyle = band.Foreground(theme.Muted).Bold(true)
	case ToShell:
		mark, markStyle = "!", band.Foreground(theme.Shell).Bold(true)
	}

	w := max(p.Width-2, 1)
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(c.Text, "\t", "    "), "\n") {
		if ansi.StringWidth(l) > w {
			l = ansi.Wrap(l, w, "")
		}
		lines = append(lines, strings.Split(l, "\n")...)
	}
	hidden := 0
	if p.Expanded == c.toggled && len(lines) > promptLines {
		hidden = len(lines) - promptLines + 1
		lines = lines[:promptLines-1]
	}

	out := make([]string, 0, len(lines)+2)
	for i, l := range lines {
		head := "  "
		if i == 0 {
			head = mark + " "
		}
		out = append(out, markStyle.Render(head)+text.Render(l)+band.Render(strings.Repeat(" ", max(w-ansi.StringWidth(l), 0))))
	}
	var notes []string
	if hidden > 0 {
		notes = append(notes, "… +"+strconv.Itoa(hidden)+" lines")
	}
	for i := range c.Images {
		notes = append(notes, "[image "+strconv.Itoa(i+1)+"]")
	}
	if len(notes) > 0 {
		note := fit(strings.Join(notes, " "), w)
		out = append(out, band.Render("  ")+band.Foreground(theme.Muted).Render(note)+band.Render(strings.Repeat(" ", max(w-ansi.StringWidth(note), 0))))
	}
	return out
}

type Assistant struct {
	rev
	still
	thinking  strings.Builder
	text      strings.Builder
	streaming bool
	toggled   bool

	// stable is the end of the streaming text's prefix that is complete
	// markdown blocks; later text can't change how it renders.
	stable      int
	stableWidth int
	stableLines []string
}

func (c *Assistant) Toggle() { c.toggled = !c.toggled; c.bump() }

func (c *Assistant) empty() bool {
	return strings.TrimSpace(c.text.String()) == "" && strings.TrimSpace(c.thinking.String()) == ""
}

func (c *Assistant) Render(p Params) []string {
	var out []string
	if thinking := strings.TrimSpace(c.thinking.String()); thinking != "" {
		out = append(out, c.renderThinking(thinking, p)...)
	}
	text := strings.TrimSpace(c.text.String())
	if text == "" {
		return out
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	head := lipgloss.NewStyle().Foreground(theme.Strong).Bold(true).Render(bullet) + " "
	return append(out, indent(c.renderText(text, p.Width-2), head)...)
}

func (c *Assistant) renderThinking(thinking string, p Params) []string {
	head := theme.SubtleText.Render("✻") + " "
	style := lipgloss.NewStyle().Foreground(theme.Subtle).Italic(true)
	if p.Expanded != c.toggled {
		return indent(markdown.Wrap(thinking, style, p.Width-2), head)
	}
	// Collapsed, show the latest line while streaming and the first once
	// done.
	line := firstLine(thinking)
	if c.streaming && strings.TrimSpace(c.text.String()) == "" {
		lines := strings.Split(thinking, "\n")
		line = strings.TrimSpace(lines[len(lines)-1])
		if line == "" && len(lines) > 1 {
			line = strings.TrimSpace(lines[len(lines)-2])
		}
	}
	label := "Thought · "
	if c.streaming {
		label = "Thinking · "
	}
	return []string{head + style.Render(fit(label+line, p.Width-2))}
}

// renderText renders complete blocks of a streaming reply only once.
func (c *Assistant) renderText(text string, width int) []string {
	if !c.streaming {
		return markdown.Render(text, width)
	}
	cut := stableCut(text)
	if cut != c.stable || width != c.stableWidth {
		c.stable, c.stableWidth = cut, width
		c.stableLines = markdown.Render(text[:cut], width)
	}
	rest := strings.TrimSpace(text[cut:])
	if cut == 0 {
		return markdown.Render(rest, width)
	}
	if rest == "" {
		return c.stableLines
	}
	out := append([]string{}, c.stableLines...)
	return append(append(out, ""), markdown.Render(rest, width)...)
}

// stableCut returns the end of the last blank line outside a code fence;
// the blocks before it are complete.
func stableCut(text string) int {
	cut, fenced, at := 0, false, 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		}
		at += len(line)
		if !fenced && trimmed == "" && strings.HasSuffix(line, "\n") && at < len(text) {
			cut = at
		}
	}
	return cut
}

type Level int

const (
	// Info is something the harness did, such as compacting.
	Info Level = iota
	Error
	// Output is what a command printed.
	Output
	Summary
)

// Notice is a message from the harness or a command, not the agent.
type Notice struct {
	rev
	still
	Level Level
	Text  string
}

func Note(text string) *Notice { return &Notice{Level: Info, Text: text} }

func Fail(text string) *Notice { return &Notice{Level: Error, Text: text} }

func Print(text string) *Notice { return &Notice{Level: Output, Text: text} }

func (c *Notice) Attached() bool { return c.Level != Summary }

func (c *Notice) Render(p Params) []string {
	text := strings.TrimRight(c.Text, "\n")
	switch c.Level {
	case Error:
		return body(markdown.Wrap(text, theme.ErrorText, bodyWidth(p.Width)), p.Width)
	case Output:
		return indent(markdown.Wrap(text, theme.Text, p.Width-2), "  ")
	case Summary:
		return markdown.Wrap(text, theme.SubtleText, p.Width)
	default:
		return body(markdown.Wrap(text, theme.MutedText, bodyWidth(p.Width)), p.Width)
	}
}
