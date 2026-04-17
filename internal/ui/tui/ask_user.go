package tui

import (
	"fmt"
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

// askUserState tracks the interactive question UI.
type askUserState struct {
	questions []tools.Question
	respCh    chan<- *tools.AskUserResponse
	width     int // terminal width for layout
	height    int // terminal height for preview limit

	current  int               // current question index
	cursor   int               // selected option index (last = "Other")
	answers  map[string]string // question text → answer
	notes    map[string]string // question text → custom note
	selected map[int]bool      // multiSelect: toggled option indices
	done     bool              // true after submit or cancel

	otherMode bool   // typing custom input
	otherBuf  string // custom input buffer

	notesMode bool   // typing notes for current option
	notesBuf  string // notes input buffer

	hasPreview bool // true if any option in current question has preview
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

	askNotesLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(Text)

	askCollapsedStyle = lipgloss.NewStyle().
				Foreground(Muted).
				Italic(true)
)

// initAskUser initializes the ask-user state from an AskUserMsg.
func initAskUser(msg AskUserMsg, width, height int) *askUserState {
	s := &askUserState{
		questions: msg.Questions,
		respCh:    msg.RespCh,
		width:     width,
		height:    height,
		answers:   make(map[string]string),
		notes:     make(map[string]string),
		selected:  make(map[int]bool),
	}
	s.detectPreview()
	return s
}

// detectPreview checks if any option in the current question has preview content.
func (s *askUserState) detectPreview() {
	s.hasPreview = false
	if s.current < len(s.questions) {
		for _, opt := range s.questions[s.current].Options {
			if opt.Preview != "" {
				s.hasPreview = true
				return
			}
		}
	}
}

// handleAskUserKey processes keyboard input when ask-user UI is active.
// Returns (handled bool, cmd tea.Cmd).
func handleAskUserKey(s *askUserState, key tea.KeyMsg) (bool, tea.Cmd) {
	q := s.questions[s.current]
	optionCount := len(q.Options) + 1 // +1 for "Other"
	if s.hasPreview && !q.MultiSelect {
		optionCount = len(q.Options) // no "Other" in preview mode
	}

	// In notes-input mode, handle typing.
	if s.notesMode {
		return handleNotesInput(s, key)
	}

	// In other-input mode, handle typing.
	if s.otherMode {
		return handleOtherInput(s, key)
	}

	switch key.String() {
	case "esc":
		close(s.respCh)
		s.done = true
		return true, nil

	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		return true, nil

	case "down", "j", "tab":
		if s.cursor < optionCount-1 {
			s.cursor++
		}
		return true, nil

	case " ":
		if q.MultiSelect && s.cursor < len(q.Options) {
			s.selected[s.cursor] = !s.selected[s.cursor]
		}
		return true, nil

	case "n":
		// Press 'n' to add notes (only in preview mode, non-multiSelect).
		if s.hasPreview && !q.MultiSelect {
			s.notesMode = true
			return true, nil
		}
		return true, nil

	case "enter":
		return handleAskUserEnter(s)
	}

	// Number shortcuts: 1-9.
	if len(key.Runes) == 1 {
		r := key.Runes[0]
		if r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < optionCount {
				s.cursor = idx
				if q.MultiSelect && idx < len(q.Options) {
					s.selected[idx] = !s.selected[idx]
					return true, nil
				}
				return handleAskUserEnter(s)
			}
		}
	}

	return true, nil // absorb all keys while ask UI is active
}

func handleOtherInput(s *askUserState, key tea.KeyMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		s.otherMode = false
		s.otherBuf = ""
		return true, nil
	case "enter":
		text := strings.TrimSpace(s.otherBuf)
		if text == "" {
			return true, nil // reject empty
		}
		s.otherMode = false
		return commitAnswer(s, text, true)
	case "backspace":
		if len(s.otherBuf) > 0 {
			// Truncate by rune for correct CJK handling.
			runes := []rune(s.otherBuf)
			s.otherBuf = string(runes[:len(runes)-1])
		}
		return true, nil
	default:
		if len(key.Runes) > 0 {
			s.otherBuf += string(key.Runes)
		}
		return true, nil
	}
}

func handleNotesInput(s *askUserState, key tea.KeyMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		s.notesMode = false
		s.notesBuf = ""
		return true, nil
	case "enter":
		text := strings.TrimSpace(s.notesBuf)
		if text != "" {
			q := s.questions[s.current]
			s.notes[q.Question] = text
		}
		s.notesMode = false
		return true, nil
	case "backspace":
		if len(s.notesBuf) > 0 {
			runes := []rune(s.notesBuf)
			s.notesBuf = string(runes[:len(runes)-1])
		}
		return true, nil
	default:
		if len(key.Runes) > 0 {
			s.notesBuf += string(key.Runes)
		}
		return true, nil
	}
}

func handleAskUserEnter(s *askUserState) (bool, tea.Cmd) {
	q := s.questions[s.current]

	// "Other" selected.
	if s.cursor == len(q.Options) {
		s.otherMode = true
		s.otherBuf = ""
		return true, nil
	}

	if q.MultiSelect {
		// Collect all selected options.
		var labels []string
		for i, opt := range q.Options {
			if s.selected[i] {
				labels = append(labels, opt.Label)
			}
		}
		if len(labels) == 0 {
			return true, nil // must select at least one
		}
		return commitAnswer(s, strings.Join(labels, ", "), false)
	}

	// Single select.
	return commitAnswer(s, q.Options[s.cursor].Label, false)
}

// commitAnswer records the answer and advances to next question or submits.
func commitAnswer(s *askUserState, answer string, isCustom bool) (bool, tea.Cmd) {
	q := s.questions[s.current]
	s.answers[q.Question] = answer
	if isCustom {
		s.notes[q.Question] = answer
	}

	// Advance to next question.
	s.current++
	if s.current < len(s.questions) {
		s.cursor = 0
		s.otherBuf = ""
		s.otherMode = false
		s.notesMode = false
		s.notesBuf = ""
		s.selected = make(map[int]bool)
		s.detectPreview()
		return true, nil
	}

	// All questions answered — send response.
	resp := &tools.AskUserResponse{
		Answers: make(map[string]string, len(s.answers)),
		Notes:   make(map[string]string, len(s.notes)),
	}
	for k, v := range s.answers {
		resp.Answers[k] = v
	}
	for k, v := range s.notes {
		resp.Notes[k] = v
	}
	s.respCh <- resp
	s.done = true
	return true, nil
}

// renderAskUser renders the question UI. If any option has a preview,
// it switches to a side-by-side layout (options left, preview right).
// Output is padded to a fixed line count to prevent bubbletea ghost renders.
func renderAskUser(s *askUserState) string {
	q := s.questions[s.current]

	// Header tag.
	var header string
	if q.Header != "" {
		headerStyle := lipgloss.NewStyle().
			Foreground(Accent).
			Bold(true).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Accent).
			Padding(0, 1)
		header = headerStyle.Render(q.Header) + "\n"
	}

	// Question text.
	questionLine := askQuestionStyle.Render(q.Question) + "\n\n"

	// Build option list.
	optionList := renderOptionList(s, q)

	// Hint line.
	hintLine := renderHintLine(s, q)

	if !s.hasPreview {
		var b strings.Builder
		b.WriteString(header)
		b.WriteString(questionLine)
		b.WriteString(optionList)
		b.WriteString("\n")
		b.WriteString(hintLine)
		return AskCardStyle.Render(b.String())
	}

	// Notes line (only in preview mode).
	var notesLine string
	if s.notesMode {
		notesLine = askNotesLabelStyle.Render("Notes: ") + s.notesBuf + "█"
	} else if note, ok := s.notes[q.Question]; ok && note != "" {
		notesLine = askNotesLabelStyle.Render("Notes: ") + askDescStyle.Render(note)
	} else {
		notesLine = askNotesLabelStyle.Render("Notes: ") + askHintStyle.Render("press n to add notes")
	}

	// Side-by-side layout with preview.
	totalWidth := max(s.width-4, 40)
	leftWidth := totalWidth * 2 / 5
	rightWidth := totalWidth - leftWidth - 2

	leftPanel := lipgloss.NewStyle().Width(leftWidth).Render(optionList)
	rightPanel := renderPreviewBox(s, q, rightWidth)
	combined := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, "  ", rightPanel)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(questionLine)
	b.WriteString(combined)
	b.WriteString("\n")
	b.WriteString(notesLine)
	b.WriteString("\n\n")
	b.WriteString(hintLine)

	content := b.String()
	// Pad to fixed height to prevent ghost renders on resize/cursor change.
	content = padToHeight(content, s.height-2)

	return AskCardStyle.Render(content)
}

// padToHeight ensures the string has exactly targetLines lines.
// Truncates if too long, pads with empty lines if too short.
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

// renderOptionList builds the numbered option list (without hints).
func renderOptionList(s *askUserState, q tools.Question) string {
	var b strings.Builder
	for i, opt := range q.Options {
		num := fmt.Sprintf("%d. ", i+1)
		if i == s.cursor {
			prefix := "> " + num
			if q.MultiSelect {
				check := "[ ] "
				if s.selected[i] {
					check = "[x] "
				}
				prefix = "> " + check + num
			}
			b.WriteString(askOptionActiveStyle.Render(prefix + opt.Label))
		} else {
			prefix := "  " + num
			if q.MultiSelect {
				check := "[ ] "
				if s.selected[i] {
					check = "[x] "
				}
				prefix = "  " + check + num
			}
			b.WriteString(askOptionInactiveStyle.Render(prefix + opt.Label))
		}
		b.WriteByte('\n')

		// Description (only in non-preview mode to save space).
		if !s.hasPreview {
			indent := "     "
			if q.MultiSelect {
				indent = "         "
			}
			b.WriteString(askDescStyle.Render(indent + opt.Description))
			b.WriteByte('\n')
		}
	}

	// Separator + "Type something" (hidden in preview+singleSelect to save space).
	if !s.hasPreview || q.MultiSelect {
		b.WriteString(askDescStyle.Render("  ───"))
		b.WriteByte('\n')
		otherIdx := len(q.Options)
		otherNum := fmt.Sprintf("%d. ", otherIdx+1)
		if s.cursor == otherIdx {
			if s.otherMode {
				b.WriteString(askOptionActiveStyle.Render("> " + otherNum + s.otherBuf + "█"))
			} else {
				b.WriteString(askOptionActiveStyle.Render("> " + otherNum + "Type something."))
			}
		} else {
			b.WriteString(askOptionInactiveStyle.Render("  " + otherNum + "Type something."))
		}
		b.WriteByte('\n')
	}

	return b.String()
}

const (
	previewMinLines = 4  // minimum lines to show
	previewOverhead = 10 // lines used by header, question, hints, notes, borders, separators
)

// previewMaxLines calculates the default collapsed line count based on terminal height.
func (s *askUserState) previewMaxLines() int {
	available := s.height - previewOverhead
	// Clamp between min and a reasonable default.
	return max(min(available, 12), previewMinLines)
}

// renderPreviewBox renders the preview content in a manually-drawn border box.
// Manual border avoids lipgloss width miscalculation with emoji/CJK characters.
func renderPreviewBox(s *askUserState, q tools.Question, width int) string {
	innerWidth := max(width-4, 16) // 2 border + 2 padding

	// Get preview for current cursor option.
	var preview string
	if s.cursor < len(q.Options) {
		preview = q.Options[s.cursor].Preview
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

// renderHintLine builds the bottom hint text.
func renderHintLine(s *askUserState, q tools.Question) string {
	if s.notesMode {
		return askHintStyle.Render("Enter to confirm · Esc to go back")
	}
	if s.otherMode {
		return askHintStyle.Render("Enter to confirm · Esc to go back")
	}
	if q.MultiSelect {
		return askHintStyle.Render("Enter to confirm · Space to toggle · ↑↓ to navigate · Esc to cancel")
	}
	if s.hasPreview {
		return askHintStyle.Render("Enter to select · ↑↓ to navigate · n to add notes · Esc to cancel")
	}
	return askHintStyle.Render("Enter to select · ↑↓ to navigate · Esc to cancel")
}
