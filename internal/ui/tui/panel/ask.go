package panel

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Ask poses the agent's questions. With several, each has a tab, and a last
// one reviews the answers before they go.
type Ask struct {
	qs    []interact.Question
	reply chan<- interact.Answers
	tab   int // a question, or len(qs) for the review
	state []answer

	input  textinput.Model
	typing bool // the user is typing their own answer
}

type answer struct {
	cursor int
	picked map[int]bool // options chosen, for a multi-select question
	custom string       // the user's own answer
	done   bool
}

// NewAsk returns the panel for qs, which answers on reply.
func NewAsk(qs []interact.Question, reply chan<- interact.Answers) *Ask {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "Type your answer"
	a := &Ask{qs: qs, reply: reply, state: make([]answer, len(qs)), input: in}
	for i := range a.state {
		a.state[i].picked = map[int]bool{}
	}
	return a
}

func (a *Ask) Key() any { return a.reply }

// rows is the number of rows question i offers: its options, then the
// user's own answer when it takes one.
func (a *Ask) rows(i int) int {
	n := len(a.qs[i].Options)
	if a.qs[i].AllowsCustom() {
		n++
	}
	return n
}

func (a *Ask) Update(msg tea.Msg) (tea.Cmd, bool) {
	if a.typing {
		return a.updateTyping(msg)
	}
	k, ok := key(msg)
	if !ok {
		return nil, false
	}
	if k == "esc" || k == "ctrl+c" {
		a.reply <- interact.Answers{Cancelled: true}
		return nil, true
	}
	if len(a.qs) > 1 {
		switch k {
		case "tab", "right":
			a.tab = (a.tab + 1) % (len(a.qs) + 1)
			return nil, false
		case "shift+tab", "left":
			a.tab = (a.tab + len(a.qs)) % (len(a.qs) + 1)
			return nil, false
		}
	}
	if a.tab == len(a.qs) {
		if k == "enter" {
			return a.submit()
		}
		return nil, false
	}

	q, s := a.qs[a.tab], &a.state[a.tab]
	n := a.rows(a.tab)
	switch k {
	case "up", "k":
		s.cursor = (s.cursor + n - 1) % n
	case "down", "j":
		s.cursor = (s.cursor + 1) % n
	case "space":
		if q.MultiSelect && s.cursor < len(q.Options) {
			s.picked[s.cursor] = !s.picked[s.cursor]
			return nil, false
		}
		return a.choose()
	case "enter":
		return a.choose()
	default:
		if i, err := strconv.Atoi(k); err == nil && i >= 1 && i <= n {
			s.cursor = i - 1
			if q.MultiSelect && s.cursor < len(q.Options) {
				s.picked[s.cursor] = !s.picked[s.cursor]
				return nil, false
			}
			return a.choose()
		}
	}
	return nil, false
}

// choose answers the current question with the row under the cursor.
func (a *Ask) choose() (tea.Cmd, bool) {
	q, s := a.qs[a.tab], &a.state[a.tab]
	if s.cursor == len(q.Options) {
		a.typing = true
		a.input.SetValue(s.custom)
		a.input.CursorEnd()
		return a.input.Focus(), false
	}
	if q.MultiSelect && !anyPicked(s.picked) {
		s.picked[s.cursor] = true
	}
	s.done = true
	return a.next()
}

func (a *Ask) updateTyping(msg tea.Msg) (tea.Cmd, bool) {
	if k, ok := key(msg); ok {
		switch k {
		case "ctrl+c":
			a.reply <- interact.Answers{Cancelled: true}
			return nil, true
		case "esc":
			a.typing = false
			a.input.Blur()
			return nil, false
		case "enter":
			a.typing = false
			a.input.Blur()
			s := &a.state[a.tab]
			s.custom = strings.TrimSpace(a.input.Value())
			if s.custom == "" {
				return nil, false
			}
			s.done = true
			return a.next()
		}
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	return cmd, false
}

// next moves to the first question left unanswered, or submits a single
// question's answer, or goes to the review.
func (a *Ask) next() (tea.Cmd, bool) {
	if len(a.qs) == 1 {
		return a.submit()
	}
	for i := range a.qs {
		if !a.state[i].done {
			a.tab = i
			return nil, false
		}
	}
	a.tab = len(a.qs)
	return nil, false
}

func (a *Ask) submit() (tea.Cmd, bool) {
	ans := interact.Answers{Selected: map[string][]string{}}
	for i, q := range a.qs {
		if a.state[i].done {
			ans.Selected[q.Question] = a.chosen(i)
		}
	}
	a.reply <- ans
	return nil, true
}

// chosen returns what question i was answered with.
func (a *Ask) chosen(i int) []string {
	q, s := a.qs[i], a.state[i]
	var out []string
	for j, o := range q.Options {
		if q.MultiSelect && s.picked[j] || !q.MultiSelect && s.cursor == j {
			out = append(out, o.Label)
		}
	}
	if s.custom != "" && (q.MultiSelect || s.cursor == len(q.Options)) {
		out = append(out, s.custom)
	}
	return out
}

func anyPicked(m map[int]bool) bool {
	for _, v := range m {
		if v {
			return true
		}
	}
	return false
}

func (a *Ask) View(width, height int) string {
	var body []string
	if len(a.qs) > 1 {
		var tabs []string
		for i, q := range a.qs {
			name := q.Header
			if name == "" {
				name = "Q" + strconv.Itoa(i+1)
			}
			if a.state[i].done {
				name = "✓ " + name
			}
			tabs = append(tabs, tabLabel(name, i == a.tab))
		}
		tabs = append(tabs, tabLabel("Submit", a.tab == len(a.qs)))
		body = append(body, strings.Join(tabs, "  "), "")
	}

	title := "Question"
	if a.tab == len(a.qs) {
		body = append(body, a.review(width)...)
		return frame(title, body, theme.Hint("enter", "submit", "tab", "switch", "esc", "cancel"), width, height)
	}

	q, s := a.qs[a.tab], a.state[a.tab]
	if q.Header != "" && len(a.qs) == 1 {
		title = q.Header
	}
	var opts []string
	for i, o := range q.Options {
		mark := strconv.Itoa(i+1) + ". "
		if q.MultiSelect {
			mark = "[ ] "
			if s.picked[i] {
				mark = "[✓] "
			}
		}
		line := row(mark+o.Label, i == s.cursor)
		if o.Description != "" {
			line += "  " + theme.SubtleText.Render(o.Description)
		}
		opts = append(opts, line)
	}
	if q.AllowsCustom() {
		i := len(q.Options)
		switch {
		case a.typing:
			a.input.SetWidth(max(width-5, 10))
			opts = append(opts, theme.Selected.Render("❯ ")+a.input.View())
		case s.custom != "":
			opts = append(opts, row(strconv.Itoa(i+1)+". "+s.custom, s.cursor == i))
		default:
			opts = append(opts, row(strconv.Itoa(i+1)+". Type your own answer", s.cursor == i))
		}
	}
	// The question may be long; the options must show.
	question := markdown.Wrap(q.Question, theme.Bold, width-2)
	if room := height - 3 - len(body) - len(opts); len(question) > room {
		question = append(question[:max(room-1, 0)], theme.SubtleText.Render("…"))
	}
	body = append(body, question...)
	body = append(body, "")
	body = append(body, opts...)
	if s.cursor < len(q.Options) && q.Options[s.cursor].Preview != "" {
		body = append(body, "")
		body = append(body, markdown.Render(q.Options[s.cursor].Preview, width-4)...)
	}

	hint := theme.Hint("↑↓", "select", "enter", "choose", "esc", "cancel")
	switch {
	case a.typing:
		hint = theme.Hint("enter", "confirm", "esc", "back")
	case q.MultiSelect:
		hint = theme.Hint("space", "toggle", "enter", "confirm", "esc", "cancel")
	}
	return frame(title, body, hint, width, height)
}

func (a *Ask) review(width int) []string {
	var out []string
	for i, q := range a.qs {
		out = append(out, markdown.Wrap(q.Question, theme.Bold, width-2)...)
		if !a.state[i].done {
			out = append(out, theme.SubtleText.Render("  (no answer)"))
			continue
		}
		out = append(out, theme.AccentText.Render("  → ")+theme.Text.Render(strings.Join(a.chosen(i), ", ")))
	}
	return out
}

func tabLabel(name string, active bool) string {
	if active {
		return theme.Selected.Underline(true).Render(name)
	}
	return theme.MutedText.Render(name)
}
