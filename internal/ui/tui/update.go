package tui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/litellm"
)

// quitResetMsg resets QuitPending after timeout.
type quitResetMsg struct{}

const completedTodosHideDelay = 5 * time.Second

const defaultPlaceholder = "Ask anything... (Enter send, Ctrl+J newline, Esc abort)"

var hideCompletedTodosTick = func(version uint64) tea.Cmd {
	return tea.Tick(completedTodosHideDelay, func(time.Time) tea.Msg {
		return hideCompletedTodosMsg{Version: version}
	})
}

// TasksTickCmd returns a tea.Cmd that fires TasksRefreshMsg after 500ms.
func TasksTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return TasksRefreshMsg{}
	})
}

// statusCountdownTick schedules the next countdown refresh.
func statusCountdownTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return statusTickMsg{}
	})
}

// Init implements tea.Model. The restored history waits for the terminal's
// size, see handleResize.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.Spinner.Tick, m.ToolSpinner.Tick, textarea.Blink)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case AgentEventMsg:
		return m.HandleAgentEvent(msg.Event)
	case CommandResultMsg:
		return m.handleCommandResult(msg)
	case ImageAttachedMsg:
		m.Pasting--
		m.Images = append(m.Images, msg.Block)
		return m, nil
	case PasteTextMsg:
		// Stays counted until the text lands, so a submit racing the paste
		// cannot send a half-populated input.
		return m, readClipboardText
	case pasteTextReadyMsg:
		m.Pasting--
		m.insertPaste(msg.Text)
		// Inserting directly skips the textarea's own key path, which is where
		// the height would otherwise be recomputed.
		m.adjustInputHeight()
		return m, nil
	case PasteErrorMsg:
		m.Pasting--
		return m, m.Emit(indentBlock(msg.Text, 2))
	case AskUserMsg:
		m.showDialog(initAskUser(msg, m.Width, m.Height))
		return m, nil
	case AskUserDismissMsg:
		m.Dialogs.dismiss(msg.RespCh)
		return m, nil
	case PermissionMsg:
		m.showDialog(initPermission(msg))
		return m, nil
	case PermissionDismissMsg:
		m.Dialogs.dismiss(msg.RespCh)
		return m, nil
	case hideCompletedTodosMsg:
		if msg.Version == m.todoHideVersion && todosFullyCompleted(m.Todos) {
			m.Todos = nil
		}
		return m, nil
	case MCPReadyMsg:
		return m.handleMCPReady(msg)
	case quitResetMsg:
		m.QuitPending = false
		return m, nil
	case SuggestionMsg:
		if !m.Running && msg.Text != "" {
			m.Suggestion = msg.Text
			m.Input.Placeholder = msg.Text
		}
		return m, nil
	case transcriptEventEnvelope:
		return m.handleTranscriptEvent(msg.Msg, msg.Ch)
	case TranscriptChannelClosedMsg:
		// Subscription dropped — usually from closeTranscriptModal calling
		// our cancel closure. If the modal happens to still be open with
		// the same target (e.g. agent disappeared without a user
		// gesture), tear it down so we don't show a frozen view.
		if m.TranscriptModal != nil && m.TranscriptAgent == msg.Agent {
			m.closeTranscriptModal()
		}
		return m, nil
	case statusTickMsg:
		if m.StatusPrefix == "" || m.StatusDeadline.IsZero() {
			return m, nil
		}
		if !time.Now().Before(m.StatusDeadline) {
			return m, nil
		}
		return m, statusCountdownTick()
	case ApplyMsg:
		return m, msg.Apply()
	case OpenedMsg:
		return m.handleOpened(msg)
	case ModeMsg:
		m.Mode = msg.Mode
		return m, nil
	case StatusChangedMsg:
		m.applyStatus(msg.Status)
		return m, nil
	case RunStartedMsg:
		m.startRun()
		return m, nil
	case IdleMsg:
		m.Running = false
		return m, nil
	case TasksRefreshMsg:
		if m.overlay() != nil {
			return m, TasksTickCmd()
		}
		return m, nil
	case spinner.TickMsg:
		var cmd1, cmd2 tea.Cmd
		m.Spinner, cmd1 = m.Spinner.Update(msg)
		m.ToolSpinner, cmd2 = m.ToolSpinner.Update(msg)
		m.RunStats.DisplayInput = animateStep(m.RunStats.DisplayInput, m.RunStats.Input)
		m.RunStats.DisplayOutput = animateStep(m.RunStats.DisplayOutput, m.RunStats.Output)
		return m, tea.Batch(cmd1, cmd2)
	}

	return m.updateInput(msg)
}

// setTodos installs a new todo list and, once every item is completed,
// schedules it to disappear.
func (m *Model) setTodos(items []todo.Item) tea.Cmd {
	m.todoHideVersion++
	m.Todos = items
	if todosFullyCompleted(items) {
		return hideCompletedTodosTick(m.todoHideVersion)
	}
	return nil
}

func todosFullyCompleted(items []todo.Item) bool {
	if len(items) == 0 {
		return false
	}
	pending, inProgress, _ := todo.Counts(items)
	return pending == 0 && inProgress == 0
}

// handleKey processes keyboard input.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if next, cmd, handled := m.handleModalKey(msg); handled {
		return next, cmd
	}
	// Transcript modal takes precedence over overlays / completions /
	// textarea so its full-screen render isn't undermined by a stray key
	// landing in the hidden input area. A dialog closes it (showDialog).
	if next, cmd, handled := m.handleTranscriptKey(msg); handled {
		return next, cmd
	}
	// Fleet-list navigation, when focus has dropped from the input into the
	// live agent roster below it. No-op (handled=false) when not focused.
	if next, cmd, handled := m.handleFleetKey(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.handleOverlayKey(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.handleSuggestionKey(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.handleCompletionKey(msg); handled {
		return next, cmd
	}

	if m.QuitPending && msg.String() != "ctrl+c" {
		m.QuitPending = false
	}

	if next, cmd, handled := m.handleCommandKey(msg); handled {
		return next, cmd
	}

	if next, cmd, handled := m.handleDropKey(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.handleImageSelectionKey(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.handlePasteRefKey(msg); handled {
		return next, cmd
	}

	switch msg.String() {
	case "ctrl+c":
		m.compActive = false
		if m.QuitPending {
			return m, tea.Quit
		}
		m.cancelRun()
		m.QuitPending = true
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
	case "esc":
		m.cancelRun()
		return m, nil
	case "alt+enter", "ctrl+j":
		m.Input.SetHeight(MaxInputLines)
		m.Input.InsertString("\n")
		m.adjustInputHeight()
		return m, nil
	case "ctrl+l":
		return m, nil
	case "ctrl+v":
		m.Pasting++
		return m, pasteClipboard
	case "enter":
		return m.handleSubmitKey()
	case "up":
		if next, cmd, handled := m.handleUpKey(); handled {
			return next, cmd
		}
	case "down":
		if next, cmd, handled := m.handleDownKey(); handled {
			return next, cmd
		}
	}

	var cmd tea.Cmd
	m.Input.SetHeight(MaxInputLines)
	m.Input, cmd = m.Input.Update(msg)
	m.adjustInputHeight()
	m.updateCompletions()
	if m.Suggestion != "" && m.Input.Value() != "" {
		m.clearSuggestion()
	}
	return m, cmd
}

// showDialog queues a dialog, leaving the transcript modal and the fleet
// list: the dialog takes the keys first, so it must not hide behind them.
func (m *Model) showDialog(card dialogCard) {
	m.fleetExit()
	m.Dialogs.push(card)
}

func (m *Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	card := m.Dialogs.active()
	if card == nil {
		return m, nil, false
	}
	handled, cmd := card.handleKey(m, msg)
	m.Dialogs.prune()
	return m, cmd, handled
}

func (m *Model) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	ov := m.overlay()
	if ov == nil {
		return m, nil, false
	}
	handled, cmd := ov.HandleKey(msg)
	if !handled {
		return m, nil, false
	}
	return m, cmd, true
}

func (m *Model) handleSuggestionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if (msg.String() != "tab" && msg.String() != "right") || m.Suggestion == "" || m.Input.Value() != "" || m.compActive {
		return m, nil, false
	}
	m.Input.SetValue(m.Suggestion)
	m.Input.CursorEnd()
	m.clearSuggestion()
	return m, nil, true
}

func (m *Model) handleCompletionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !m.compActive {
		return m, nil, false
	}
	switch msg.String() {
	case "tab":
		m.acceptCompletion()
		return m, nil, true
	case "enter":
		item := m.acceptCompletion()
		if !item.AutoExecute {
			return m, nil, true
		}
		return m, nil, false
	case "up":
		if m.compIdx > 0 {
			m.compIdx--
		}
		return m, nil, true
	case "down":
		if m.compIdx < len(m.compItems)-1 {
			m.compIdx++
		}
		return m, nil, true
	case "esc":
		m.compActive = false
		return m, nil, true
	default:
		return m, nil, false
	}
}

// handleDropKey takes the terminal's bracketed-paste event. Anything that is
// not a droppable image path is pasted text and must go through insertPaste —
// falling through to the textarea would insert an oversized paste verbatim,
// bypassing the reference. Ctrl+V is a separate path (PasteTextMsg) that lands
// in the same place.
func (m *Model) handleDropKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !msg.Paste {
		return m, nil, false
	}
	if cmd := dropImage(string(msg.Runes)); cmd != nil {
		m.Pasting++
		return m, cmd, true
	}
	m.insertPaste(string(msg.Runes))
	// Inserting directly skips the textarea's own key path, which is where the
	// height would otherwise be recomputed.
	m.adjustInputHeight()
	return m, nil, true
}

func (m *Model) handleImageSelectionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.ImageCursor < 0 {
		return m, nil, false
	}
	switch msg.String() {
	case "left", "h":
		if m.ImageCursor > 0 {
			m.ImageCursor--
		}
		return m, nil, true
	case "right", "l":
		if m.ImageCursor < len(m.Images)-1 {
			m.ImageCursor++
		}
		return m, nil, true
	case "delete", "backspace":
		m.Images = slices.Delete(m.Images, m.ImageCursor, m.ImageCursor+1)
		if len(m.Images) == 0 {
			m.ImageCursor = -1
		} else if m.ImageCursor >= len(m.Images) {
			m.ImageCursor = len(m.Images) - 1
		}
		return m, nil, true
	case "esc", "down":
		m.ImageCursor = -1
		return m, nil, true
	default:
		m.ImageCursor = -1
		return m, nil, false
	}
}

func (m *Model) handleSubmitKey() (tea.Model, tea.Cmd) {
	req, ok := m.prepareSubmission()
	if !ok {
		return m, nil
	}

	output := m.RenderPromptOutput(req.displayText)
	m.ShowWelcome = false
	if m.Running {
		// The conversation takes it into the run at its next step.
		m.QueuedMsgs = append(m.QueuedMsgs, req.text)
	}
	return m, tea.Sequence(m.printBlock(output), m.submit(req.text, req.images))
}

type submitRequest struct {
	text        string
	images      []litellm.Block
	displayText string
}

func (m *Model) prepareSubmission() (submitRequest, bool) {
	if m.Pasting > 0 {
		return submitRequest{}, false
	}

	text := strings.TrimSpace(m.Input.Value())
	if text == "" && m.Suggestion != "" && len(m.Images) == 0 {
		text = m.Suggestion
		m.clearSuggestion()
	}
	if text == "" && len(m.Images) == 0 {
		m.Input.Reset()
		m.Input.SetHeight(1)
		return submitRequest{}, false
	}

	if text != "" {
		// Stored with its paste bodies, or recalling it in a later session
		// would send the bare reference as literal text.
		m.history.Add(text, m.pastedFor(text))
	}
	m.histIdx = -1
	m.histDraft = ""

	req := submitRequest{
		// The model gets the full bodies; the transcript keeps the references,
		// so a 50KB paste does not replay into scrollback.
		text:   m.expandPasteRefs(text),
		images: m.Images,
	}
	req.displayText = formatSubmitDisplayText(text, req.images)

	m.Images = nil
	m.ImageCursor = -1
	m.Input.Reset()
	m.Input.SetHeight(1)
	m.Input.Placeholder = ""

	return req, true
}

func formatSubmitDisplayText(text string, images []litellm.Block) string {
	if len(images) == 0 {
		return text
	}
	tags := make([]string, 0, len(images))
	for i := range images {
		tags = append(tags, fmt.Sprintf("[Image #%d]", i+1))
	}
	if text == "" {
		return strings.Join(tags, " ")
	}
	return text + " " + strings.Join(tags, " ")
}

func (m *Model) handleMCPReady(msg MCPReadyMsg) (tea.Model, tea.Cmd) {
	var parts []string
	if msg.Tools > 0 {
		parts = append(parts, fmt.Sprintf("%d tools connected", msg.Tools))
	}
	for _, e := range msg.Errors {
		parts = append(parts, ErrorStyle.Render(e))
	}
	if len(parts) == 0 {
		return m, nil
	}
	text := MutedStyle.Render("  mcp: ") + strings.Join(parts, MutedStyle.Render(", "))
	return m, m.Emit(text)
}

func (m *Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.Input.SetHeight(MaxInputLines)
	m.Input, cmd = m.Input.Update(msg)
	m.adjustInputHeight()
	return m, cmd
}

func (m *Model) handleUpKey() (tea.Model, tea.Cmd, bool) {
	if len(m.Images) > 0 && !m.Running && m.Input.Line() == 0 {
		m.ImageCursor = len(m.Images) - 1
		return m, nil, true
	}
	if m.history.Len() == 0 || m.Input.Line() != 0 || m.Running {
		return m, nil, false
	}
	if m.histIdx == -1 {
		m.histDraft = m.Input.Value()
		m.histIdx = 0
	} else if m.histIdx < m.history.Len()-1 {
		m.histIdx++
	}
	m.Input.Reset()
	m.Input.SetValue(m.recallHistory(m.histIdx))
	m.Input.CursorEnd()
	m.adjustInputHeight()
	return m, nil, true
}

func (m *Model) handleDownKey() (tea.Model, tea.Cmd, bool) {
	atLastLine := m.Input.Line() == m.Input.LineCount()-1
	// History forward-navigation takes priority while browsing history.
	if m.histIdx >= 0 && atLastLine {
		if m.histIdx > 0 {
			m.histIdx--
			m.Input.Reset()
			m.Input.SetValue(m.recallHistory(m.histIdx))
		} else {
			m.histIdx = -1
			m.Input.Reset()
			m.Input.SetValue(m.histDraft)
			m.histDraft = ""
		}
		m.Input.CursorEnd()
		m.adjustInputHeight()
		return m, nil, true
	}
	// Otherwise ↓ at the last input line drops focus into the live agent list
	// below the input (only when something is live, matching what's rendered).
	// Consumes the key so the textarea doesn't also react.
	if atLastLine && m.fleetEnterable() {
		m.FleetFocus = true
		m.FleetCursor = 1 // land on the first agent; "main" sits above at row 0
		m.Input.Blur()    // keyboard belongs to the list now — stop the input cursor
		return m, nil, true
	}
	return m, nil, false
}

// handleResize processes terminal resize events.
//
// On width change or height shrink we wipe the viewport *and* the OS
// scrollback (`\x1b[2J\x1b[3J\x1b[H`) and then replay every cached scrollback
// body via a single tea.Println. This exterminates resize ghosts at the
// source: bubbletea's line-based cursor tracking cannot untangle terminal
// reflow, so instead of patching the delta we rebuild the whole stream.
//
// Why this works where tea.ClearScreen did not:
//
//   - Direct stdout write is synchronous with Update — no race with the
//     ticker flushing a stale frame between WindowSizeMsg and the async
//     clearScreenMsg.
//   - `\x1b[3J` wipes the OS scrollback copy that terminals populate when
//     reflow pushes old wide lines upward; without it every resize left
//     duplicates stacked in scrollback.
//   - The replayed bodies are byte-identical to the originals. Joining
//     with "\n" is equivalent to the original per-block Println sequence
//     because tea.Println splits on "\n" internally (see
//     standard_renderer.go printLineMessage). Content that overflows the
//     viewport scrolls into the newly-empty OS scrollback naturally — so
//     mouse-wheel history survives the resize (it is just rewritten).
//
// Skipped on the first WindowSizeMsg (prev width == 0) — nothing to clear
// and the scrollback cache is empty anyway.
func (m *Model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	prevWidth, prevHeight := m.Width, m.Height

	m.Width = msg.Width
	m.Height = msg.Height
	m.Ready = true
	m.Input.SetWidth(m.Width - 2)
	if m.Markdown == nil {
		m.Markdown = markdown.NewRenderer(max(m.Width-6, 20))
	} else {
		m.Markdown.SetWidth(max(m.Width-6, 20))
	}
	m.adjustInputHeight()
	m.transcriptOnResize()

	if prevWidth == 0 {
		// The first size: the history renders at the terminal's width.
		return m.handleRestore()
	}
	needsReplay := prevWidth != 0 && (prevWidth != m.Width || m.Height < prevHeight)
	if !needsReplay {
		return m, nil
	}

	_, _ = os.Stdout.WriteString("\x1b[2J\x1b[3J\x1b[H")
	if len(m.Scrollback) == 0 {
		return m, nil
	}
	return m, tea.Println(strings.Join(m.Scrollback, "\n"))
}

// clearSuggestion removes the current prompt suggestion and clears the placeholder.
func (m *Model) clearSuggestion() {
	if m.Suggestion == "" {
		return
	}
	m.Suggestion = ""
	m.Input.Placeholder = ""
}

// adjustInputHeight grows/shrinks the textarea to fit the content,
// accounting for both explicit newlines and soft-wrapping.
func (m *Model) adjustInputHeight() {
	w := m.Input.Width()
	if w <= 0 {
		w = 1
	}
	lines := 0
	for _, line := range strings.Split(m.Input.Value(), "\n") {
		visualLen := lipgloss.Width(line)
		if visualLen == 0 {
			lines++
		} else {
			lines += (visualLen + w - 1) / w
		}
	}
	lines = max(lines, 1)
	lines = min(lines, MaxInputLines)
	m.Input.SetHeight(lines)
}

// handleCommandResult processes slash command results.
func (m *Model) handleCommandResult(msg CommandResultMsg) (tea.Model, tea.Cmd) {
	if msg.Quit {
		return m, tea.Quit
	}
	if msg.Text != "" {
		var output string
		if m.ShowWelcome {
			output = m.renderWelcome() + "\n"
			m.ShowWelcome = false
		}
		output += indentBlock(msg.Text, 2)
		if msg.Inline {
			return m, m.printInline(output)
		}
		return m, m.printBlock(output)
	}
	return m, nil
}

// submit sends the user's input to the conversation, which starts a run or
// takes it into the live one. UserPromptSubmit hooks run on the way, so it
// happens off the TUI's goroutine.
func (m *Model) submit(text string, images []litellm.Block) tea.Cmd {
	if text == "" && len(images) > 0 {
		text = "Describe this image"
	}
	blocks := append([]litellm.Block{litellm.Text(text)}, images...)
	conv := m.conv
	return func() tea.Msg {
		if err := conv.Submit(context.Background(), blocks); err != nil {
			return CommandResultMsg{Text: ErrorStyle.Render(app.ErrorText(err))}
		}
		return nil
	}
}

// cancelRun stops the conversation's run, if one is live.
func (m *Model) cancelRun() {
	if m.Running {
		m.conv.Cancel()
	}
}

// handleOpened shows a conversation that replaced the open one: a cleared
// screen, then its history.
func (m *Model) handleOpened(msg OpenedMsg) (tea.Model, tea.Cmd) {
	m.closeTranscriptModal()
	m.fleetExit()
	m.Dialogs.abortAll()
	m.Running = false
	m.Todos = nil
	m.Images = nil
	m.ImageCursor = -1
	m.QueuedMsgs = nil
	m.ShowWelcome = true
	m.clearSuggestion()
	m.Scrollback = nil
	_, _ = os.Stdout.WriteString("\x1b[2J\x1b[3J\x1b[H")
	m.open(msg.Conversation)
	return m.handleRestore()
}

// updateCompletions refreshes the completion menu based on current input.
func (m *Model) updateCompletions() {
	m.cmdHighlight = ""
	text := m.Input.Value()
	if !strings.HasPrefix(text, "/") {
		m.compActive = false
		return
	}

	if strings.ContainsAny(text, " \t") {
		m.compActive = false
		cmd := text[1:]
		if idx := strings.IndexAny(cmd, " \t"); idx > 0 {
			cmd = cmd[:idx]
		}
		items := m.commands.Complete(cmd)
		for _, item := range items {
			if strings.EqualFold(item.Name, cmd) {
				m.cmdHighlight = "/" + item.Name
				break
			}
		}
		return
	}

	prefix := text[1:]
	items := m.commands.Complete(prefix)
	m.compItems = items
	m.compActive = len(items) > 0
	if m.compIdx >= len(items) {
		m.compIdx = max(len(items)-1, 0)
	}
	for _, item := range items {
		if strings.EqualFold(item.Name, prefix) {
			m.cmdHighlight = "/" + item.Name
			break
		}
	}
}

// acceptCompletion fills the selected completion into the input.
func (m *Model) acceptCompletion() CompletionItem {
	if !m.compActive || m.compIdx < 0 || m.compIdx >= len(m.compItems) {
		return CompletionItem{}
	}
	item := m.compItems[m.compIdx]
	name := item.Name
	m.Input.Reset()
	m.Input.SetValue("/" + name + " ")
	m.Input.CursorEnd()
	m.compActive = false
	m.cmdHighlight = "/" + name
	return item
}

// animateStep moves current one step closer to target.
// Uses ~8% of the remaining gap per tick (30fps), min step 1.
func animateStep(current, target int) int {
	if current >= target {
		return target
	}
	step := max((target-current)/12, 1)
	return min(current+step, target)
}
