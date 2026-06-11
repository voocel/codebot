package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
	cbteam "github.com/voocel/codebot/internal/team"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
)

// quitResetMsg resets QuitPending after timeout.
type quitResetMsg struct{}

const completedTasksHideDelay = 5 * time.Second

const defaultPlaceholder = "Ask anything... (Enter send, Ctrl+J newline, Esc abort)"

var hideCompletedTasksTick = func(version uint64) tea.Cmd {
	return tea.Tick(completedTasksHideDelay, func(time.Time) tea.Msg {
		return HideCompletedTasksMsg{Version: version}
	})
}

// TasksTickCmd returns a tea.Cmd that fires TasksRefreshMsg after 500ms.
func TasksTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return TasksRefreshMsg{}
	})
}

// retryCountdownTick schedules the next retry-countdown re-render.
// 500ms keeps the integer-second display visually responsive without
// per-frame churn (View() ceil-rounds the remaining duration).
func retryCountdownTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return retryTickMsg{}
	})
}

// RestoreMsg is sent to replay restored session messages into scrollback.
type RestoreMsg struct{ Msgs []agentcore.AgentMessage }

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.Spinner.Tick, m.ToolSpinner.Tick, textarea.Blink}
	if len(m.config.RestoredMessages) > 0 {
		msgs := m.config.RestoredMessages
		cmds = append(cmds, func() tea.Msg { return RestoreMsg{Msgs: msgs} })
	}
	if m.Tasks != nil && tasksFullyCompleted(*m.Tasks) && m.taskHideVersion > 0 {
		cmds = append(cmds, hideCompletedTasksTick(m.taskHideVersion))
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case RestoreMsg:
		return m.handleRestore(msg)
	case AgentEventMsg:
		return m.HandleAgentEvent(msg.Event)
	case CommandResultMsg:
		return m.handleCommandResult(msg)
	case PromptMsg:
		return m.handlePrompt(msg)
	case ImageAttachedMsg:
		m.Pasting--
		m.Images = append(m.Images, msg.Block)
		return m, nil
	case PasteTextMsg:
		m.Pasting--
		return m, textarea.Paste
	case PasteErrorMsg:
		m.Pasting--
		return m, m.Emit(indentBlock(msg.Text, 2))
	case AskUserMsg:
		m.AskUser = initAskUser(msg, m.Width, m.Height)
		return m, nil
	case PermissionMsg:
		m.Permission = initPermission(msg)
		if msg.Tool == planExitToolName {
			// Push the full plan into scrollback so it stays visible
			// regardless of length; the approval card itself only shows
			// the title + two options and always fits on screen.
			body := renderPlanScrollback(msg.Preview, m.Markdown, m.Width)
			return m, m.Emit(formatScrollbackBlock(body, false))
		}
		return m, nil
	case PermissionDismissMsg:
		m.Permission = nil
		return m, nil
	case TaskListUpdateMsg:
		return m, m.applyTaskSnapshot(msg.Snapshot)
	case HideCompletedTasksMsg:
		if msg.Version != m.taskHideVersion || m.Tasks == nil || !tasksFullyCompleted(*m.Tasks) {
			return m, nil
		}
		var cmd tea.Cmd
		if m.config.OnHideCompletedTasks != nil {
			cmd = m.config.OnHideCompletedTasks(*m.Tasks)
		}
		m.Tasks = nil
		return m, cmd
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
		// the same target (e.g. teammate disappeared without a user
		// gesture), tear it down so we don't show a frozen view.
		if m.TranscriptModal != nil && m.TranscriptAgent == msg.Agent {
			m.closeTranscriptModal()
		}
		return m, nil
	case RetryStatusMsg:
		m.RetryPrefix = msg.Prefix
		m.RetryDeadline = msg.Deadline
		if msg.Prefix == "" {
			return m, nil
		}
		return m, retryCountdownTick()
	case retryTickMsg:
		if m.RetryPrefix == "" || m.RetryDeadline.IsZero() {
			return m, nil
		}
		if !time.Now().Before(m.RetryDeadline) {
			return m, nil
		}
		return m, retryCountdownTick()
	case BtwResultMsg:
		if m.config.OnBtwResult != nil {
			m.config.OnBtwResult(msg)
		}
		return m, nil
	case TasksRefreshMsg:
		if m.config.Overlay != nil {
			if ov := m.config.Overlay(m); ov != nil {
				return m, TasksTickCmd()
			}
		}
		return m, nil
	case taskRecencyTickMsg:
		if m.Tasks == nil {
			return m, nil
		}
		// Returning the snapshot scheduler hands us either a follow-up tick
		// (more recent completes still pending expiry) or nil; either way
		// the View re-renders this frame because we returned via Update.
		return m, scheduleRecencyTick(*m.Tasks)
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

func (m *Model) applyTaskSnapshot(snap storage.TaskSnapshot) tea.Cmd {
	m.taskHideVersion++
	if snap.Total == 0 {
		m.Tasks = nil
		return nil
	}
	snapshot := snap
	m.Tasks = &snapshot
	if tasksFullyCompleted(snap) {
		return hideCompletedTasksTick(m.taskHideVersion)
	}
	if cmd := scheduleRecencyTick(snap); cmd != nil {
		return cmd
	}
	return nil
}

// scheduleRecencyTick returns a tea.Cmd that fires once when the next
// "recently completed" task crosses RecentCompletedTTL — only when the list
// is actually being truncated (otherwise the recency grouping is unused, so
// no re-render is needed). Returns nil when there's nothing to wake up for.
func scheduleRecencyTick(snap storage.TaskSnapshot) tea.Cmd {
	if snap.Total <= taskTreeMaxVisible {
		return nil
	}
	now := time.Now()
	var nextExpiry time.Time
	for _, t := range snap.Items {
		if t.Status != storage.TaskCompleted || t.CompletedAt == nil {
			continue
		}
		expiry := t.CompletedAt.Add(RecentCompletedTTL)
		if !expiry.After(now) {
			continue
		}
		if nextExpiry.IsZero() || expiry.Before(nextExpiry) {
			nextExpiry = expiry
		}
	}
	if nextExpiry.IsZero() {
		return nil
	}
	// 50ms floor protects against tea.Tick busy-looping if the deadline is
	// already past or fires marginally early.
	delay := max(time.Until(nextExpiry), 50*time.Millisecond)
	return tea.Tick(delay, func(time.Time) tea.Msg { return taskRecencyTickMsg{} })
}

func tasksFullyCompleted(snap storage.TaskSnapshot) bool {
	return snap.Total > 0 && snap.Pending == 0 && snap.InProgress == 0
}

// handleKey processes keyboard input.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if next, cmd, handled := m.handleModalKey(msg); handled {
		return next, cmd
	}
	// Transcript modal takes precedence over overlays / completions /
	// textarea so its full-screen render isn't undermined by a stray key
	// landing in the hidden input area. It runs AFTER AskUser/Permission
	// because those dialogs require user input and the transcript modal is
	// read-only — we never want to block them.
	if next, cmd, handled := m.handleTranscriptKey(msg); handled {
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

	if m.config.OnKey != nil {
		if handled, cmd := m.config.OnKey(m, msg); handled {
			return m, cmd
		}
	}

	if next, cmd, handled := m.handleDropKey(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.handleImageSelectionKey(msg); handled {
		return next, cmd
	}

	switch msg.String() {
	case "ctrl+c":
		m.compActive = false
		if m.QuitPending {
			return m, tea.Quit
		}
		if m.Running && m.Driver != nil {
			m.Driver.Abort()
		}
		m.QuitPending = true
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
	case "esc":
		if m.Running && m.Driver != nil {
			m.Driver.Abort()
		}
		return m, nil
	case "alt+enter", "ctrl+j":
		m.Input.SetHeight(MaxInputLines)
		m.Input.InsertString("\n")
		m.adjustInputHeight()
		return m, nil
	case "ctrl+l":
		return m, nil
	case "ctrl+v":
		if m.config.OnPaste != nil {
			m.Pasting++
			return m, m.config.OnPaste(m)
		}
		var cmd tea.Cmd
		m.Input.SetHeight(MaxInputLines)
		m.Input, cmd = m.Input.Update(msg)
		m.adjustInputHeight()
		return m, cmd
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

func (m *Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.AskUser != nil {
		// Ctrl+C aborts the entire turn: drop the channel (handler returns
		// canceled error → model sees a degraded prompt) and stop the agent.
		// Esc, by contrast, sends a Cancelled response with any partial
		// answers so the model can continue with what it learned. The two
		// gestures are deliberately distinct.
		if msg.String() == "ctrl+c" {
			close(m.AskUser.respCh)
			m.AskUser = nil
			if m.Running && m.Driver != nil {
				m.Driver.Abort()
			}
			return m, nil, true
		}
		handled, cmd := handleAskUserKey(m.AskUser, msg)
		if handled {
			if m.AskUser.done {
				m.AskUser = nil
			}
			return m, cmd, true
		}
	}

	if m.Permission == nil {
		return m, nil, false
	}
	if msg.String() == "ctrl+c" || msg.String() == "esc" {
		m.Permission.respCh <- PermitChoiceDeny
		m.Permission = nil
		return m, nil, true
	}
	handled, cmd := handlePermissionKey(m.Permission, msg)
	if handled {
		if m.Permission.done {
			m.Permission = nil
		}
		return m, cmd, true
	}
	return m, nil, false
}

func (m *Model) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.config.Overlay == nil {
		return m, nil, false
	}
	ov := m.config.Overlay(m)
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

func (m *Model) handleDropKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !msg.Paste || m.config.OnDrop == nil {
		return m, nil, false
	}
	cmd := m.config.OnDrop(m, string(msg.Runes))
	if cmd == nil {
		return m, nil, false
	}
	m.Pasting++
	return m, cmd, true
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
	if m.Driver == nil {
		output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: session driver is not configured", 2)), 2)
	} else if m.Running {
		m.Images = req.images
		m.Driver.Steer(req.text)
		m.QueuedMsgs = append(m.QueuedMsgs, req.text)
	} else if err := m.promptWithImages(req.text, req.images); err != nil {
		output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: "+err.Error(), 2)), 2)
	}
	return m, m.printBlock(output)
}

type submitRequest struct {
	text        string
	images      []agentcore.ContentBlock
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

	if m.history != nil && text != "" {
		m.history.Add(text)
	}
	m.histIdx = -1
	m.histDraft = ""

	req := submitRequest{
		text:   text,
		images: m.Images,
	}
	req.displayText = formatSubmitDisplayText(req.text, req.images)

	m.Images = nil
	m.ImageCursor = -1
	m.Input.Reset()
	m.Input.SetHeight(1)
	m.Input.Placeholder = ""

	return req, true
}

func formatSubmitDisplayText(text string, images []agentcore.ContentBlock) string {
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
	m.MCPLoading = false
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
	if m.history == nil || m.history.Len() == 0 || m.Input.Line() != 0 || m.Running {
		return m, nil, false
	}
	if m.histIdx == -1 {
		m.histDraft = m.Input.Value()
		m.histIdx = 0
	} else if m.histIdx < m.history.Len()-1 {
		m.histIdx++
	}
	m.Input.Reset()
	m.Input.SetValue(m.history.Get(m.histIdx))
	m.Input.CursorEnd()
	m.adjustInputHeight()
	return m, nil, true
}

func (m *Model) handleDownKey() (tea.Model, tea.Cmd, bool) {
	if m.histIdx < 0 || m.Input.Line() != m.Input.LineCount()-1 {
		return m, nil, false
	}
	if m.histIdx > 0 {
		m.histIdx--
		m.Input.Reset()
		m.Input.SetValue(m.history.Get(m.histIdx))
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
	if m.AskUser != nil {
		m.AskUser.width = m.Width
		m.AskUser.height = m.Height
	}
	m.transcriptOnResize()

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
	if msg.Clear {
		m.TurnCount = 0
		m.ShowWelcome = true
		m.Images = nil
		// Drop the scrollback cache so a subsequent resize doesn't replay
		// content the user just asked to clear. The terminal viewport is
		// cleared by the command itself; this keeps the model consistent.
		m.Scrollback = nil
	}
	if msg.NewProvider != "" {
		m.Provider = msg.NewProvider
	}
	if msg.NewModel != "" {
		m.ModelName = msg.NewModel
	}
	if msg.NewContextWindow > 0 {
		m.ContextWindow = msg.NewContextWindow
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

// handlePrompt processes an injected prompt — renders as user message and sends to agent.
func (m *Model) handlePrompt(msg PromptMsg) (tea.Model, tea.Cmd) {
	text := msg.Text
	if text == "" {
		return m, nil
	}

	output := m.RenderPromptOutput(text)
	m.ShowWelcome = false
	if m.Driver == nil {
		output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: session driver is not configured", 2)), 2)
	} else if err := m.promptWithImages(text, nil); err != nil {
		output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: "+err.Error(), 2)), 2)
	}
	return m, m.printBlock(output)
}

// promptWithImages sends user text with optional clipboard image attachments.
// Falls back to plain text prompt when no images are present.
func (m *Model) promptWithImages(text string, images []agentcore.ContentBlock) error {
	if len(images) == 0 {
		return m.Driver.Prompt(text)
	}
	if text == "" {
		text = "Describe this image"
	}
	blocks := make([]agentcore.ContentBlock, 0, 1+len(images))
	blocks = append(blocks, agentcore.TextBlock(text))
	blocks = append(blocks, images...)
	return m.Driver.PromptWithBlocks(blocks)
}

// handleRestore replays restored session messages into terminal scrollback.
// All history is rendered as a single tea.Println to guarantee display order.
// Renders the same way as live events: user messages, thinking, tool calls/results, assistant text.
func (m *Model) handleRestore(msg RestoreMsg) (tea.Model, tea.Cmd) {
	m.ShowWelcome = false

	var sb strings.Builder
	sb.WriteString(m.renderWelcome())
	sb.WriteString("\n\n")
	sb.WriteString(MutedStyle.Render("  ── restored session ──"))

	toolCalls := buildRestoreToolIndex(msg.Msgs)

	for _, am := range msg.Msgs {
		m.appendRestoredMessage(&sb, am, toolCalls)
	}

	sb.WriteString("\n\n")
	sb.WriteString(MutedStyle.Render("  ── end of history ──"))

	// Teams are pure in-memory state — they cannot survive a process restart.
	// If the prior session created one, the agent's history still references
	// teammates that no longer exist (any send_message to them will fail).
	// Surface a one-line hint so the user knows to recreate the team if they
	// still need it, instead of discovering the loss via an obscure error.
	if name := lastTeamNameFromHistory(msg.Msgs); name != "" {
		sb.WriteString("\n\n")
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("  ⓘ Team %q from the previous session is not active. Recreate it with team_create if you still need it.", name)))
	}

	return m, m.Emit(sb.String())
}

// lastTeamNameFromHistory scans assistant tool_use blocks in restored history
// for the most recent team_create call and returns the team_name argument.
// Returns "" when no team was created in the prior session.
//
// Walks in reverse so a session that recreated the team after a previous
// team_dismiss still surfaces the latest name. Cheap because tool_use blocks
// already live on the assistant Message — no extra parsing of tool results
// (which would also need their own indexing).
func lastTeamNameFromHistory(msgs []agentcore.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		concrete, ok := msgs[i].(agentcore.Message)
		if !ok || concrete.GetRole() != agentcore.RoleAssistant {
			continue
		}
		for _, tc := range concrete.ToolCalls() {
			if tc.Name != "team_create" {
				continue
			}
			var a struct {
				TeamName string `json:"team_name"`
			}
			if err := json.Unmarshal(tc.Args, &a); err == nil && a.TeamName != "" {
				return a.TeamName
			}
		}
	}
	return ""
}

type restoredToolCall struct {
	name string
	args json.RawMessage
}

func buildRestoreToolIndex(msgs []agentcore.AgentMessage) map[string]restoredToolCall {
	toolCalls := make(map[string]restoredToolCall)
	for _, am := range msgs {
		concrete, ok := am.(agentcore.Message)
		if !ok || concrete.GetRole() != agentcore.RoleAssistant {
			continue
		}
		for _, tc := range concrete.ToolCalls() {
			toolCalls[tc.ID] = restoredToolCall{name: tc.Name, args: tc.Args}
		}
	}
	return toolCalls
}

func (m *Model) appendRestoredMessage(sb *strings.Builder, am agentcore.AgentMessage, toolCalls map[string]restoredToolCall) {
	switch am.GetRole() {
	case agentcore.RoleUser:
		m.appendRestoredUserMessage(sb, am)
	case agentcore.RoleAssistant:
		m.appendRestoredAssistantMessage(sb, am)
	case agentcore.RoleTool:
		m.appendRestoredToolMessage(sb, am, toolCalls)
	}
}

func (m *Model) appendRestoredUserMessage(sb *strings.Builder, am agentcore.AgentMessage) {
	text := restoredUserText(am)
	if text == "" {
		return
	}
	sb.WriteString("\n\n")
	// Teammate-injected user messages must restore with the same purple-bubble
	// styling they had live, not as a "this is what you typed" echo.
	if from, body, ok := cbteam.ParseTeammateAttachment(text); ok && body != "" {
		sb.WriteString(m.renderTeammateMessage(from, body))
		return
	}
	sb.WriteString(m.renderUserMessage(text))
}

func (m *Model) appendRestoredAssistantMessage(sb *strings.Builder, am agentcore.AgentMessage) {
	if thinkingText := strings.TrimSpace(am.ThinkingContent()); thinkingText != "" {
		indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinkingText, 2)), 2)
		sb.WriteString("\n\n")
		sb.WriteString(ThinkingBodyStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
	}
	if content := strings.TrimSpace(am.TextContent()); content != "" {
		indented := m.RenderMarkdownBlock(content, 2)
		sb.WriteString("\n\n")
		sb.WriteString(AssistantIconStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
	}
	concrete, ok := am.(agentcore.Message)
	if !ok {
		return
	}
	for _, tc := range concrete.ToolCalls() {
		if IsHiddenToolCall(tc.Name, tc.Args) {
			continue
		}
		header := ToolIconStyle.Render("● ") + RenderToolHeader(tc.Name, tc.Args)
		sb.WriteString("\n")
		sb.WriteString(header)
	}
}

func (m *Model) appendRestoredToolMessage(sb *strings.Builder, am agentcore.AgentMessage, toolCalls map[string]restoredToolCall) {
	concrete, ok := am.(agentcore.Message)
	if !ok {
		return
	}
	toolCallID, _ := concrete.Metadata["tool_call_id"].(string)
	toolCall := toolCalls[toolCallID]
	if IsHiddenToolCall(toolCall.name, toolCall.args) {
		return
	}
	isError, _ := concrete.Metadata["is_error"].(bool)
	body := m.renderRestoredToolBody(toolCall.name, toolCall.args, restoredToolResultRaw(concrete), isError)
	if body == "" {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(body)
}

func restoredUserText(am agentcore.AgentMessage) string {
	msg, ok := am.(agentcore.Message)
	if !ok {
		return stripLeadingHarnessBlocks(am.TextContent())
	}
	if msg.Metadata["injected"] == true && strings.Contains(msg.TextContent(), "<system-reminder>") {
		return ""
	}
	var last string
	textBlocks := 0
	for _, block := range msg.Content {
		if block.Type == agentcore.ContentText && block.Text != "" {
			textBlocks++
			last = block.Text
		}
	}
	if textBlocks == 1 && strings.Contains(last, "<system-reminder>") {
		last = stripLeadingHarnessBlocks(last)
	}
	return stripLeadingHarnessBlocks(last)
}

func stripLeadingHarnessBlocks(text string) string {
	for {
		text = strings.TrimLeft(text, " \t\r\n")
		stripped := false
		for _, tag := range []string{"system-reminder", "available-deferred-tools", "context-summary"} {
			open := "<" + tag + ">"
			if !strings.HasPrefix(text, open) {
				continue
			}
			close := "</" + tag + ">"
			end := strings.Index(text[len(open):], close)
			if end < 0 {
				return ""
			}
			text = text[len(open)+end+len(close):]
			stripped = true
			break
		}
		if !stripped {
			return text
		}
	}
}

func restoredToolResultRaw(msg agentcore.Message) json.RawMessage {
	text := msg.TextContent()
	raw := json.RawMessage(text)
	if json.Valid(raw) {
		return raw
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return encoded
}

func (m *Model) renderRestoredToolBody(toolName string, args, raw json.RawMessage, isError bool) string {
	switch {
	case toolName == "subagent" && !isError:
		content := FormatSubagentOutput(raw)
		return indentBlock(m.renderSubagentCard(content), 2)
	case toolName == "edit" && !isError:
		return indentBlock(RenderEditResult(raw, extractPathArg(args), m.diffBodyWidth()), 2)
	case toolName == "write" && !isError:
		return indentBlock(RenderWriteResult(raw), 2)
	case (toolName == "read" || toolName == "glob") && !isError:
		return indentBlock(RenderReadResult(raw), 2)
	case toolName == "ls" && !isError:
		_, lsBody := RenderLsResult(raw)
		return indentBlock(lsBody, 2)
	default:
		text := FormatToolResult(raw, isError)
		text = m.wrapTextForIndent(text, 4)
		if isError {
			return indentBlock(FormatToolOutput(text, ToolResultMaxLines, ErrorStyle), 2)
		}
		return indentBlock(FormatToolOutput(text, ToolResultMaxLines), 2)
	}
}

// updateCompletions refreshes the completion menu based on current input.
func (m *Model) updateCompletions() {
	m.cmdHighlight = ""
	if m.config.Completions == nil {
		m.compActive = false
		return
	}
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
		items := m.config.Completions(cmd)
		for _, item := range items {
			if strings.EqualFold(item.Name, cmd) {
				m.cmdHighlight = "/" + item.Name
				break
			}
		}
		return
	}

	prefix := text[1:]
	items := m.config.Completions(prefix)
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
