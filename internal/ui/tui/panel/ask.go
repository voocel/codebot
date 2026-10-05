package panel

import (
	"strconv"
	"strings"

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
	head  head
	queue
	own field // where the user types their own answer
}

// answer is where a question stands: its rows are its options, then the
// user's own answer when it takes one.
type answer struct {
	menu
	picked map[int]bool // options chosen, for a multi-select question
	custom string       // the user's own answer
	done   bool
}

// NewAsk returns the panel for qs, which answers on reply.
func NewAsk(qs []interact.Question, reply chan<- interact.Answers) *Ask {
	a := &Ask{qs: qs, reply: reply, state: make([]answer, len(qs)), own: newField("Type your answer")}
	for i, q := range qs {
		n := len(q.Options)
		if q.AllowsCustom() {
			n++
		}
		a.state[i] = answer{menu: menu{n: n}, picked: map[int]bool{}}
	}
	return a
}

func (a *Ask) Key() any { return a.reply }

func (a *Ask) Update(msg tea.Msg) (tea.Cmd, bool) {
	if a.own.on {
		return a.typed(msg)
	}
	k, ok := key(msg)
	if !ok || a.head.key(k) {
		return nil, false
	}
	if k == "esc" || k == "ctrl+c" {
		a.reply <- interact.Answers{Cancelled: true}
		return nil, true
	}
	if len(a.qs) > 1 {
		switch k {
		case "tab", "right":
			a.tab, a.head = (a.tab+1)%(len(a.qs)+1), head{}
			return nil, false
		case "shift+tab", "left":
			a.tab, a.head = (a.tab+len(a.qs))%(len(a.qs)+1), head{}
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
	if k == "space" {
		k = "enter"
		if q.MultiSelect {
			k = strconv.Itoa(s.cursor + 1)
		}
	}
	i := s.key(k)
	switch {
	case i < 0:
		return nil, false
	case q.MultiSelect && k != "enter" && i < len(q.Options):
		// A number, or space, ticks an option of several.
		s.picked[i] = !s.picked[i]
		return nil, false
	}
	return a.choose()
}

// choose answers the current question with the row under the cursor.
func (a *Ask) choose() (tea.Cmd, bool) {
	q, s := a.qs[a.tab], &a.state[a.tab]
	if s.cursor == len(q.Options) {
		return a.own.open(s.custom), false
	}
	if q.MultiSelect && !anyPicked(s.picked) {
		s.picked[s.cursor] = true
	}
	s.done = true
	return a.next()
}

// typed takes msg while the user types their own answer.
func (a *Ask) typed(msg tea.Msg) (tea.Cmd, bool) {
	if k, _ := key(msg); k == "ctrl+c" {
		a.reply <- interact.Answers{Cancelled: true}
		return nil, true
	}
	cmd, text, entered := a.own.update(msg)
	if !entered {
		return cmd, false
	}
	s := &a.state[a.tab]
	if s.custom = text; text == "" {
		return nil, false
	}
	s.done = true
	return a.next()
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
		return frame(a.queue.title(title), body, theme.Hint("enter", "submit", "tab", "switch", "esc", "cancel"), width, height)
	}

	q, s := a.qs[a.tab], a.state[a.tab]
	if q.Header != "" && len(a.qs) == 1 {
		title = q.Header
	}
	var opts []string
	for i, o := range q.Options {
		line := s.numbered(i, o.Label)
		if q.MultiSelect {
			mark := "[ ] "
			if s.picked[i] {
				mark = "[✓] "
			}
			line = row(mark+o.Label, i == s.cursor)
		}
		if o.Description != "" {
			line += "  " + theme.SubtleText.Render(o.Description)
		}
		opts = append(opts, line)
	}
	if q.AllowsCustom() {
		i := len(q.Options)
		switch {
		case a.own.on:
			opts = append(opts, a.own.view(width))
		case s.custom != "":
			opts = append(opts, s.numbered(i, s.custom))
		default:
			opts = append(opts, s.numbered(i, "Type your own answer"))
		}
	}
	if s.cursor < len(q.Options) && q.Options[s.cursor].Preview != "" {
		opts = append(opts, "")
		opts = append(opts, markdown.Render(q.Options[s.cursor].Preview, width-4)...)
	}
	// The question may be long; the options must show.
	body, cut := a.head.fit(append(body, markdown.Wrap(q.Question, theme.Bold, width-2)...), opts, height-2)

	hint := []string{"↑↓", "select", "enter", "choose"}
	switch {
	case a.own.on:
		hint = []string{"enter", "confirm", "esc", "back"}
	case q.MultiSelect:
		hint = []string{"space", "toggle", "enter", "confirm"}
	}
	if cut {
		hint = append(hint, "pgup/pgdn", "scroll")
	}
	if !a.own.on {
		hint = append(hint, "esc", "cancel")
	}
	return frame(a.queue.title(title), body, theme.Hint(hint...), width, height)
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
