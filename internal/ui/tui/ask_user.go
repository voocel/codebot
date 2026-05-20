package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/tools"
)

// AskUserMsg is sent by the AskUser handler to show questions in the TUI.
type AskUserMsg struct {
	Questions []tools.Question
	RespCh    chan<- *tools.AskUserResponse
}

// askUserState tracks the interactive multi-question UI.
//
// The state machine has two axes:
//
//  1. tab: 0..len(questions)-1 = each question; len(questions) = Confirm page.
//     For "single" questions (one question, non-multi-select) we skip the
//     Confirm page — a one-shot pick should not require a second keypress.
//  2. per-question state in perQ[tab]: selection bitmap, custom input buffer,
//     and edit-mode flag. Keeping these per-tab lets users freely revisit
//     earlier questions via Tab / Shift-Tab without losing prior input.
type askUserState struct {
	questions []tools.Question
	respCh    chan<- *tools.AskUserResponse
	width     int
	height    int

	tab  int             // 0..len(questions)-1 = question; len(questions) = Confirm
	perQ []questionState // one entry per question
	done bool            // true after submit or cancel response sent
}

// questionState tracks UI state for a single question.
type questionState struct {
	cursor    int          // option index under highlight (0..len(options)+customExtra-1)
	selected  []string     // committed labels (single = at most 1; multi = N)
	otherBuf  string       // text typed into "Type your own answer"
	otherMode bool         // currently editing the custom buffer
	picked    map[int]bool // multi-select: which listed options are checked
}

// AskUser styles.
var (
	askQuestionStyle = lipgloss.NewStyle().
				Foreground(Title).
				Bold(true)

	askOptionActiveStyle = lipgloss.NewStyle().
				Foreground(Accent).
				Bold(true)

	askOptionInactiveStyle = lipgloss.NewStyle().
				Foreground(Text)

	askDescStyle = lipgloss.NewStyle().
			Foreground(Muted)

	askHintStyle = lipgloss.NewStyle().
			Foreground(Muted).
			Italic(true)

	askWarnStyle = lipgloss.NewStyle().
			Foreground(Danger).
			Italic(true)

	askTabActiveStyle = lipgloss.NewStyle().
				Foreground(Accent).
				Bold(true).
				Padding(0, 1)

	askTabAnsweredStyle = lipgloss.NewStyle().
				Foreground(Text).
				Padding(0, 1)

	askTabPendingStyle = lipgloss.NewStyle().
				Foreground(Muted).
				Padding(0, 1)

	askCollapsedStyle = lipgloss.NewStyle().
				Foreground(Muted).
				Italic(true)
)

// initAskUser builds initial state for the dialog.
func initAskUser(msg AskUserMsg, width, height int) *askUserState {
	s := &askUserState{
		questions: msg.Questions,
		respCh:    msg.RespCh,
		width:     width,
		height:    height,
		perQ:      make([]questionState, len(msg.Questions)),
	}
	for i := range s.perQ {
		s.perQ[i].picked = make(map[int]bool)
	}
	return s
}

// isSingle is true when only one non-multi question is asked: we skip the
// Confirm tab entirely and commit on first Enter.
func (s *askUserState) isSingle() bool {
	return len(s.questions) == 1 && !s.questions[0].MultiSelect
}

// tabCount returns the number of tabs (questions + optional Confirm).
func (s *askUserState) tabCount() int {
	if s.isSingle() {
		return 1
	}
	return len(s.questions) + 1
}

// onConfirm reports whether the active tab is the Confirm review page.
func (s *askUserState) onConfirm() bool {
	return !s.isSingle() && s.tab == len(s.questions)
}

// activeQuestion returns the question for the current tab (zero value at Confirm).
func (s *askUserState) activeQuestion() tools.Question {
	if s.onConfirm() {
		return tools.Question{}
	}
	return s.questions[s.tab]
}

// activeQState returns mutable per-question state (nil at Confirm).
func (s *askUserState) activeQState() *questionState {
	if s.onConfirm() {
		return nil
	}
	return &s.perQ[s.tab]
}

// optionCount returns total selectable rows for the active question, including
// the host-rendered "Type your own answer" entry when allowed.
func (s *askUserState) optionCount() int {
	q := s.activeQuestion()
	n := len(q.Options)
	if q.AllowsCustom() {
		n++
	}
	return n
}

// onCustomRow is true when the cursor sits on the "Type your own answer" row.
func (s *askUserState) onCustomRow() bool {
	q := s.activeQuestion()
	if !q.AllowsCustom() {
		return false
	}
	st := s.activeQState()
	if st == nil {
		return false
	}
	return st.cursor == len(q.Options)
}

// hasPreview is true if any listed option in the active question has preview content.
func (s *askUserState) hasPreview() bool {
	if s.onConfirm() {
		return false
	}
	for _, opt := range s.questions[s.tab].Options {
		if opt.Preview != "" {
			return true
		}
	}
	return false
}

// handleAskUserKey processes input when the ask-user UI is active.
// Returns (handled, cmd). Always handled to absorb keys away from the input area.
func handleAskUserKey(s *askUserState, key tea.KeyMsg) (bool, tea.Cmd) {
	// Sub-mode: typing into "Type your own answer"
	if st := s.activeQState(); st != nil && st.otherMode {
		return handleOtherInput(s, st, key)
	}

	switch key.String() {
	case "esc":
		s.cancel()
		return true, nil

	case "tab", "right", "l":
		s.moveTab(+1)
		return true, nil

	case "shift+tab", "left", "h":
		s.moveTab(-1)
		return true, nil

	case "up", "k":
		if st := s.activeQState(); st != nil && st.cursor > 0 {
			st.cursor--
		}
		return true, nil

	case "down", "j":
		if st := s.activeQState(); st != nil && st.cursor < s.optionCount()-1 {
			st.cursor++
		}
		return true, nil

	case " ":
		// Space toggles multi-select picks on listed options.
		if q := s.activeQuestion(); q.MultiSelect {
			st := s.activeQState()
			if st != nil && st.cursor < len(q.Options) {
				st.picked[st.cursor] = !st.picked[st.cursor]
				st.selected = collectMultiPicks(q, st)
			}
		}
		return true, nil

	case "enter":
		return handleAskUserEnter(s)
	}

	// Number shortcuts: 1-9 jumps and acts.
	if len(key.Runes) == 1 {
		r := key.Runes[0]
		if r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < s.optionCount() {
				st := s.activeQState()
				if st == nil {
					return true, nil
				}
				st.cursor = idx
				q := s.activeQuestion()
				if q.MultiSelect && idx < len(q.Options) {
					st.picked[idx] = !st.picked[idx]
					st.selected = collectMultiPicks(q, st)
					return true, nil
				}
				return handleAskUserEnter(s)
			}
		}
	}

	return true, nil
}

func handleOtherInput(s *askUserState, st *questionState, key tea.KeyMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		st.otherMode = false
		st.otherBuf = ""
		return true, nil
	case "enter":
		text := strings.TrimSpace(st.otherBuf)
		if text == "" {
			st.otherMode = false
			return true, nil
		}
		q := s.activeQuestion()
		if q.MultiSelect {
			// Custom text joins the multi-select pick set.
			st.selected = append(collectMultiPicks(q, st), text)
		} else {
			st.selected = []string{text}
		}
		st.otherMode = false
		if s.isSingle() {
			return s.submit(), nil
		}
		s.advanceOrConfirm()
		return true, nil
	case "backspace":
		if len(st.otherBuf) > 0 {
			runes := []rune(st.otherBuf)
			st.otherBuf = string(runes[:len(runes)-1])
		}
		return true, nil
	default:
		if len(key.Runes) > 0 {
			st.otherBuf += string(key.Runes)
		}
		return true, nil
	}
}

func handleAskUserEnter(s *askUserState) (bool, tea.Cmd) {
	if s.onConfirm() {
		return s.submit(), nil
	}

	q := s.activeQuestion()
	st := s.activeQState()
	if st == nil {
		return true, nil
	}

	// Cursor on "Type your own answer": enter edit mode.
	if s.onCustomRow() {
		st.otherMode = true
		st.otherBuf = ""
		return true, nil
	}

	if q.MultiSelect {
		// Enter on a listed option toggles it; advancing is via Tab or
		// pressing Enter again with a non-empty pick set on the last tab.
		st.picked[st.cursor] = !st.picked[st.cursor]
		st.selected = collectMultiPicks(q, st)
		return true, nil
	}

	// Single-select on a listed option commits this question.
	st.selected = []string{q.Options[st.cursor].Label}
	if s.isSingle() {
		return s.submit(), nil
	}
	s.advanceOrConfirm()
	return true, nil
}

// collectMultiPicks returns labels for currently checked listed options,
// preserving original option order.
func collectMultiPicks(q tools.Question, st *questionState) []string {
	out := make([]string, 0, len(st.picked))
	for i, opt := range q.Options {
		if st.picked[i] {
			out = append(out, opt.Label)
		}
	}
	return out
}

// advanceOrConfirm moves to the next un-answered question, or to the Confirm
// tab when none remain. Wraps to Confirm at the end.
func (s *askUserState) advanceOrConfirm() {
	for i := s.tab + 1; i < len(s.questions); i++ {
		if len(s.perQ[i].selected) == 0 {
			s.tab = i
			return
		}
	}
	s.tab = len(s.questions) // Confirm tab
}

// moveTab navigates by delta with wraparound across the question/Confirm tabs.
func (s *askUserState) moveTab(delta int) {
	if s.isSingle() {
		return
	}
	n := s.tabCount()
	s.tab = ((s.tab+delta)%n + n) % n
}

// submit packages answers and sends them on the channel, then marks done.
// We do not block partial or empty submissions: the user has seen the
// Confirm page (which already flags unanswered questions in red) and chose
// to submit anyway. That choice belongs to the user; the model receives a
// degraded result ("User provided no answers..." or only the questions that
// were answered) and decides what to do from there.
// Returns true (handled). Caller should not touch state after this.
func (s *askUserState) submit() bool {
	resp := s.buildResponse(false)
	s.respCh <- resp
	s.done = true
	return true
}

// cancel sends a Cancelled response with any partial answers and marks done.
func (s *askUserState) cancel() {
	resp := s.buildResponse(true)
	s.respCh <- resp
	s.done = true
}

func (s *askUserState) buildResponse(cancelled bool) *tools.AskUserResponse {
	answers := make(map[string][]string, len(s.questions))
	notes := make(map[string]string)
	for i, q := range s.questions {
		st := &s.perQ[i]
		if len(st.selected) > 0 {
			answers[q.Question] = append([]string(nil), st.selected...)
		}
		if note := strings.TrimSpace(st.otherBuf); note != "" && slices.Contains(st.selected, note) {
			// Surface the typed text as a note only when it was actually
			// chosen — otherwise it's a stale buffer the user backed out of.
			notes[q.Question] = note
		}
	}
	return &tools.AskUserResponse{
		Answers:   answers,
		Notes:     notes,
		Cancelled: cancelled,
	}
}

// renderAskUser composes the full dialog. Two layouts:
//
//   - Non-preview question (or Confirm page): single-column option list.
//   - Preview-enabled question: side-by-side (options left, preview right).
//
// Content is padded to a fixed line count to avoid BubbleTea ghost renders
// on resize/cursor change.
func renderAskUser(s *askUserState) string {
	var b strings.Builder

	// Tab bar at the top (skipped for single-question dialogs).
	if tabBar := s.renderTabBar(); tabBar != "" {
		b.WriteString(tabBar)
		b.WriteString("\n\n")
	}

	if s.onConfirm() {
		b.WriteString(s.renderConfirm())
	} else {
		b.WriteString(s.renderQuestion())
	}

	b.WriteString("\n")
	b.WriteString(renderHintLine(s))

	content := b.String()
	// Only pad when the preview pane is in play: its height varies with the
	// focused option's content, so we lock the frame to the tallest possible
	// preview to avoid the right column jiggling and AskCardStyle reflowing.
	// Multi-question dialogs already render a stable layout per tab — padding
	// them to terminal height would inflate the bordered card to the whole
	// screen, leaving empty space framed below the content.
	if s.hasPreview() {
		content = padToHeight(content, s.previewMaxLines()+6)
	}
	return AskCardStyle.Render(content)
}

func (s *askUserState) renderTabBar() string {
	if s.isSingle() {
		return ""
	}
	chips := make([]string, 0, s.tabCount())
	for i, q := range s.questions {
		label := q.Header
		if label == "" {
			label = fmt.Sprintf("Q%d", i+1)
		}
		answered := len(s.perQ[i].selected) > 0
		switch {
		case i == s.tab:
			chips = append(chips, askTabActiveStyle.Render(label))
		case answered:
			chips = append(chips, askTabAnsweredStyle.Render(label+" ✓"))
		default:
			chips = append(chips, askTabPendingStyle.Render(label))
		}
	}
	// Confirm tab.
	if s.tab == len(s.questions) {
		chips = append(chips, askTabActiveStyle.Render("Confirm"))
	} else {
		chips = append(chips, askTabPendingStyle.Render("Confirm"))
	}
	return strings.Join(chips, " ")
}

func (s *askUserState) renderQuestion() string {
	q := s.activeQuestion()
	var b strings.Builder
	suffix := ""
	if q.MultiSelect {
		suffix = askDescStyle.Render("  (select all that apply)")
	}
	b.WriteString(askQuestionStyle.Render(q.Question) + suffix + "\n\n")

	optionList := s.renderOptionList()

	if !s.hasPreview() {
		b.WriteString(optionList)
		return b.String()
	}

	// Side-by-side: options on the left, preview on the right.
	totalWidth := max(s.width-4, 40)
	leftWidth := totalWidth * 2 / 5
	rightWidth := totalWidth - leftWidth - 2
	leftPanel := lipgloss.NewStyle().Width(leftWidth).Render(optionList)
	rightPanel := s.renderPreviewBox(rightWidth)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, "  ", rightPanel))
	return b.String()
}

func (s *askUserState) renderOptionList() string {
	q := s.activeQuestion()
	st := s.activeQState()
	var b strings.Builder

	for i, opt := range q.Options {
		row := s.renderOptionRow(q, st, i, opt.Label)
		b.WriteString(row)
		b.WriteByte('\n')
		// Only show option description when no preview pane competes for room.
		if !s.hasPreview() {
			indent := "     "
			if q.MultiSelect {
				indent = "         "
			}
			b.WriteString(askDescStyle.Render(indent + opt.Description))
			b.WriteByte('\n')
		}
	}

	if q.AllowsCustom() {
		b.WriteString(askDescStyle.Render("  ───"))
		b.WriteByte('\n')
		b.WriteString(s.renderCustomRow(q, st))
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *askUserState) renderOptionRow(q tools.Question, st *questionState, i int, label string) string {
	num := fmt.Sprintf("%d. ", i+1)
	active := i == st.cursor
	check := ""
	if q.MultiSelect {
		if st.picked[i] {
			check = "[x] "
		} else {
			check = "[ ] "
		}
	}
	prefix := "  " + check + num
	if active {
		prefix = "> " + check + num
		return askOptionActiveStyle.Render(prefix + label)
	}
	return askOptionInactiveStyle.Render(prefix + label)
}

func (s *askUserState) renderCustomRow(q tools.Question, st *questionState) string {
	idx := len(q.Options)
	num := fmt.Sprintf("%d. ", idx+1)
	active := st.cursor == idx
	label := "Type your own answer"
	if st.otherMode {
		label = st.otherBuf + "█"
	} else if st.otherBuf != "" {
		label = st.otherBuf
	}
	check := ""
	if q.MultiSelect {
		// Custom row is "picked" when otherBuf was committed into selected.
		picked := false
		for _, sel := range st.selected {
			if sel == strings.TrimSpace(st.otherBuf) && sel != "" {
				picked = true
				break
			}
		}
		if picked {
			check = "[x] "
		} else {
			check = "[ ] "
		}
	}
	if active {
		return askOptionActiveStyle.Render("> " + check + num + label)
	}
	return askOptionInactiveStyle.Render("  " + check + num + label)
}

const (
	previewMinLines = 4
	previewOverhead = 10
)

func (s *askUserState) previewMaxLines() int {
	available := s.height - previewOverhead
	return max(min(available, 12), previewMinLines)
}

func (s *askUserState) renderPreviewBox(width int) string {
	innerWidth := max(width-4, 16)
	q := s.activeQuestion()
	st := s.activeQState()
	var preview string
	if st != nil && st.cursor < len(q.Options) {
		preview = q.Options[st.cursor].Preview
	}
	maxLines := s.previewMaxLines()
	if preview == "" {
		return DrawBox([]string{askDescStyle.Render("No preview available")}, innerWidth, maxLines)
	}
	lines := strings.Split(preview, "\n")
	var collapsedLine string
	if len(lines) > maxLines {
		hidden := len(lines) - maxLines
		lines = lines[:maxLines]
		collapsedLine = askCollapsedStyle.Render(fmt.Sprintf("── %d lines hidden ──", hidden))
	}
	box := DrawBox(lines, innerWidth, maxLines)
	if collapsedLine != "" {
		box += "\n" + collapsedLine
	}
	return box
}

func (s *askUserState) renderConfirm() string {
	var b strings.Builder
	b.WriteString(askQuestionStyle.Render("Review your answers") + "\n")

	// Persistent unanswered warning: visible the moment the user lands on
	// Confirm, so a stray Enter on an incomplete answer set isn't silent.
	// Submission is still allowed — this only flags, doesn't block.
	unanswered := 0
	for i := range s.questions {
		if len(s.perQ[i].selected) == 0 {
			unanswered++
		}
	}
	if unanswered > 0 {
		b.WriteString(askWarnStyle.Render(fmt.Sprintf("⚠ %d of %d questions not answered", unanswered, len(s.questions))))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	for i, q := range s.questions {
		header := q.Header
		if header == "" {
			header = fmt.Sprintf("Q%d", i+1)
		}
		b.WriteString(askOptionInactiveStyle.Render("• " + header + ": "))
		selected := s.perQ[i].selected
		if len(selected) == 0 {
			b.WriteString(askWarnStyle.Render("(not answered)"))
		} else {
			b.WriteString(askOptionActiveStyle.Render(strings.Join(selected, ", ")))
		}
		b.WriteByte('\n')
		b.WriteString(askDescStyle.Render("  " + q.Question))
		b.WriteByte('\n')
	}
	return b.String()
}

func renderHintLine(s *askUserState) string {
	if st := s.activeQState(); st != nil && st.otherMode {
		return askHintStyle.Render("Enter confirm · Esc cancel edit")
	}
	if s.onConfirm() {
		return askHintStyle.Render("Enter submit · ⇆ Tab/Shift-Tab back to questions · Esc cancel")
	}
	q := s.activeQuestion()
	parts := []string{"↑↓ navigate"}
	if q.MultiSelect {
		parts = append(parts, "Space/Enter toggle")
	} else {
		parts = append(parts, "Enter pick")
	}
	if !s.isSingle() {
		parts = append(parts, "Tab next")
	}
	parts = append(parts, "Esc cancel")
	return askHintStyle.Render(strings.Join(parts, " · "))
}

// padToHeight pads or truncates s to exactly targetLines lines, preventing
// BubbleTea ghost frames when the dialog shrinks on cursor change.
func padToHeight(s string, targetLines int) string {
	if targetLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > targetLines {
		lines = lines[:targetLines]
	}
	for len(lines) < targetLines {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
