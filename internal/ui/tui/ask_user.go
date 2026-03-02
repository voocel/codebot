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

	current  int               // current question index
	cursor   int               // selected option index (last = "Other")
	answers  map[string]string // question text → answer
	notes    map[string]string // question text → custom note
	selected map[int]bool      // multiSelect: toggled option indices
	done     bool              // true after submit or cancel

	otherMode bool   // typing custom input
	otherBuf  string // custom input buffer
}

// AskUser styles.
var (
	askQuestionStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Bold(true)

	askOptionActiveStyle = lipgloss.NewStyle().
				Foreground(ColorAccent).
				Bold(true)

	askOptionInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	askDescStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	askHintStyle = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Italic(true)
)

// initAskUser initializes the ask-user state from an AskUserMsg.
func initAskUser(msg AskUserMsg) *askUserState {
	return &askUserState{
		questions: msg.Questions,
		respCh:   msg.RespCh,
		answers:  make(map[string]string),
		notes:    make(map[string]string),
		selected: make(map[int]bool),
	}
}

// handleAskUserKey processes keyboard input when ask-user UI is active.
// Returns (handled bool, cmd tea.Cmd).
func handleAskUserKey(s *askUserState, key tea.KeyMsg) (bool, tea.Cmd) {
	q := s.questions[s.current]
	optionCount := len(q.Options) + 1 // +1 for "Other"

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
		s.selected = make(map[int]bool)
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

// renderAskUser renders the question UI (no border, minimal style).
func renderAskUser(s *askUserState) string {
	q := s.questions[s.current]

	var b strings.Builder

	// Question text (bold white).
	b.WriteString(askQuestionStyle.Render(q.Question))
	b.WriteString("\n\n")

	// Numbered options.
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
		// Description indented under label.
		indent := "     "
		if q.MultiSelect {
			indent = "         "
		}
		b.WriteString(askDescStyle.Render(indent + opt.Description))
		b.WriteByte('\n')
	}

	// Separator before "Type something".
	b.WriteString(askDescStyle.Render("  ───"))
	b.WriteByte('\n')

	// "Type something." option (like Claude Code's).
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
	b.WriteString("\n\n")

	// Hint line.
	if s.otherMode {
		b.WriteString(askHintStyle.Render("Enter to confirm · Esc to go back"))
	} else if q.MultiSelect {
		b.WriteString(askHintStyle.Render("Enter to confirm · Space to toggle · ↑↓ Navigate · Esc to cancel"))
	} else {
		b.WriteString(askHintStyle.Render("Enter to select · ↑↓ Navigate · Esc to cancel"))
	}

	return indentBlock(b.String(), 2)
}
