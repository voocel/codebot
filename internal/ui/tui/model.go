package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage" // input history
	"github.com/voocel/codebot/internal/tools"
)

// PlanBarInfo provides plan mode status for the status bar.
type PlanBarInfo struct {
	Tag     string   // appended to status bar (e.g., "plan mode")
	Choices []string // selectable options
	Active  int      // active choice index

	// Plan review card fields.
	Prompt    string // e.g., "Would you like to proceed?"
	OtherMode bool   // typing custom feedback
	OtherBuf  string // custom feedback buffer
}

// Config provides hooks for extending the base TUI behavior.
type Config struct {
	Placeholder      string
	Version          string
	Cwd              string
	GitBranch        string
	EnvHint          string                   // shown below welcome when using env var credentials
	History          *storage.History         // input history (Up/Down navigation)
	RestoredMessages []agentcore.AgentMessage // messages restored from a previous session (rendered on Init)
	OnKey            func(m *Model, msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	OnEvent          func(m *Model, ev agentcore.Event) tea.Cmd
	OnPaste          func(m *Model) tea.Cmd              // Ctrl+V: read clipboard image, return ImageAttachedMsg
	OnDrop           func(m *Model, text string) tea.Cmd // Drag-drop: if text is image path, return cmd; else nil
	OnMCPReady       func(msg MCPReadyMsg)               // called when MCP servers finish connecting
	StatusRight      func(m *Model) string
	StatusMode       func(m *Model) string            // mode indicator for context bar (e.g. "⏵⏵ trust")
	StatusPlan       func(m *Model) *PlanBarInfo
	Overlay          func(m *Model) *OverlayState         // interactive command overlay
	Completions      func(prefix string) []CompletionItem // slash command completions
}

// CompletionItem is a single command completion candidate.
type CompletionItem struct {
	Name        string // command name without "/" (e.g. "model")
	Description string
	Usage       string
	Kind        string
	Category    string
	NeedsIdle   bool
	Source      string
	Aliases     []string
	AutoExecute bool
}

// OverlayState bridges an interactive command overlay to the TUI.
type OverlayState struct {
	HandleKey func(msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	View      func(width int) string
}

// Driver defines the minimal conversation operations required by the TUI.
type Driver interface {
	Prompt(text string) error
	PromptWithBlocks(blocks []agentcore.ContentBlock) error
	Steer(text string)
	Abort()
}

// runStats tracks per-run statistics displayed after agent completion.
type runStats struct {
	Turns     int
	ToolCalls int
	Input     int
	Output    int
	StartedAt time.Time
	Duration  time.Duration

	// Animated display counters: smoothly count up toward Input/Output.
	DisplayInput  int
	DisplayOutput int
}

// Model is the bubbletea Model for the agent TUI.
// Completed content is printed to terminal scrollback via tea.Println;
// View() only renders the live area (status + streaming + input).
type Model struct {
	Driver    Driver
	ModelName string
	Version   string

	Input   textarea.Model
	Spinner spinner.Model

	ToolSpinner spinner.Model // breathing-dot spinner for tool execution

	Streaming *strings.Builder
	Thinking  *strings.Builder
	IsStream  bool

	Running         bool
	TurnCount       int
	PendingTools    map[string]string           // toolID -> tool name
	ToolHeaders     map[string]string           // toolID -> formatted header (printed at end)
	ToolOutputBuf   map[string]*strings.Builder // toolID -> streaming output
	ToolDeltaBuf    map[string]*strings.Builder // toolID -> accumulated subagent delta text
	ToolThinkingBuf map[string]*strings.Builder // toolID -> accumulated subagent thinking text

	Width  int
	Height int
	Ready  bool

	Cwd         string
	GitBranch   string
	ShowWelcome bool
	EnvHint     string // env var hint shown below welcome
	ShowSummary bool
	RunStats    runStats
	Images      []agentcore.ContentBlock // attached images (from Ctrl+V clipboard paste)
	ImageCursor int                      // -1 = not selecting; 0+ = selected image index
	Pasting     int                      // number of async image reads in progress (clipboard paste or drag-drop)

	Glamour *glamour.TermRenderer
	config  Config

	AskUser    *askUserState    // non-nil when ask-user UI is active
	Permission *permissionState // non-nil when permission prompt is active

	Tasks *tools.TaskSnapshot // non-nil when tasks exist; displayed above input

	QueuedMsgs []string // messages queued while agent is running (display only)

	MCPLoading bool // true while MCP servers are connecting in background

	compItems  []CompletionItem // current completion candidates
	compIdx    int              // selected completion index
	compActive bool             // completion menu visible

	QuitPending bool // true after first Ctrl+C, waiting for second to quit

	history   *storage.History // input history store (nil = disabled)
	histIdx   int              // -1 = not browsing; 0+ = current position (0 = most recent)
	histDraft string           // stashed input before history navigation
}

// New creates a Model with the given agent, model name, and optional config.
func New(driver Driver, modelName string, cfg ...Config) Model {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}

	sp := spinner.New()
	sp.Spinner = spinner.Spinner{
		Frames: []string{"·", "✢", "✶", "✽", "✶", "✢", "·"},
		FPS:    time.Second / 30, // 30fps for smooth shimmer
	}
	sp.Style = lipgloss.NewStyle().Foreground(ColorAssistant)

	tsp := spinner.New()
	tsp.Spinner = spinner.Spinner{
		Frames: []string{"●", "◉", "○", "◉"},
		FPS:    time.Second / 4,
	}
	tsp.Style = lipgloss.NewStyle().Foreground(ColorTool)

	ta := textarea.New()
	placeholder := "Ask anything... (Enter send, Ctrl+J newline, Esc abort)"
	if c.Placeholder != "" {
		placeholder = c.Placeholder
	}
	ta.Placeholder = placeholder
	ta.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return "❯ "
		}
		return "  "
	})
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(ColorMuted) // 243, medium gray
	ta.Focus()
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0

	return Model{
		Driver:          driver,
		ModelName:       modelName,
		Version:         c.Version,
		Spinner:         sp,
		ToolSpinner:     tsp,
		Input:           ta,
		Streaming:       &strings.Builder{},
		Thinking:        &strings.Builder{},
		PendingTools:    make(map[string]string),
		ToolHeaders:     make(map[string]string),
		ToolOutputBuf:   make(map[string]*strings.Builder),
		ToolDeltaBuf:    make(map[string]*strings.Builder),
		ToolThinkingBuf: make(map[string]*strings.Builder),
		Cwd:             c.Cwd,
		GitBranch:       c.GitBranch,
		ShowWelcome:     true,
		EnvHint:         c.EnvHint,
		ImageCursor:     -1,
		config:          c,
		history:         c.History,
		histIdx:         -1,
	}
}

// quitResetMsg resets QuitPending after timeout.
type quitResetMsg struct{}

// TasksTickCmd returns a tea.Cmd that fires TasksRefreshMsg after 500ms.
func TasksTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return TasksRefreshMsg{}
	})
}

// RestoreMsg is sent to replay restored session messages into scrollback.
type RestoreMsg struct{ Msgs []agentcore.AgentMessage }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.Spinner.Tick, m.ToolSpinner.Tick, textarea.Blink}
	if len(m.config.RestoredMessages) > 0 {
		msgs := m.config.RestoredMessages
		cmds = append(cmds, func() tea.Msg { return RestoreMsg{Msgs: msgs} })
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		// Clipboard had no image — delegate to textarea for text paste.
		m.Pasting--
		return m, textarea.Paste
	case PasteErrorMsg:
		m.Pasting--
		return m, tea.Println(indentBlock(msg.Text, 2))
	case AskUserMsg:
		m.AskUser = initAskUser(msg, m.Width, m.Height)
		return m, nil
	case PermissionMsg:
		m.Permission = initPermission(msg)
		return m, nil
	case PermissionDismissMsg:
		m.Permission = nil
		return m, nil
	case TaskListUpdateMsg:
		if msg.Snapshot.Total == 0 {
			m.Tasks = nil
		} else {
			snap := msg.Snapshot
			m.Tasks = &snap
		}
		return m, nil
	case MCPReadyMsg:
		m.MCPLoading = false
		if m.config.OnMCPReady != nil {
			m.config.OnMCPReady(msg)
		}
		var parts []string
		if msg.Tools > 0 {
			parts = append(parts, fmt.Sprintf("%d tools connected", msg.Tools))
		}
		for _, e := range msg.Errors {
			parts = append(parts, ErrorStyle.Render(e))
		}
		if len(parts) > 0 {
			text := MutedStyle.Render("  mcp: ") + strings.Join(parts, MutedStyle.Render(", "))
			return m, tea.Println(text)
		}
		return m, nil
	case quitResetMsg:
		m.QuitPending = false
		return m, nil
	case TasksRefreshMsg:
		// Overlay re-renders on every View() call; just schedule the next tick
		// if an overlay is still active.
		if m.config.Overlay != nil {
			if ov := m.config.Overlay(&m); ov != nil {
				return m, TasksTickCmd()
			}
		}
		return m, nil
	case spinner.TickMsg:
		var cmd1, cmd2 tea.Cmd
		m.Spinner, cmd1 = m.Spinner.Update(msg)
		m.ToolSpinner, cmd2 = m.ToolSpinner.Update(msg)
		// Animate token counters toward real values.
		m.RunStats.DisplayInput = animateStep(m.RunStats.DisplayInput, m.RunStats.Input)
		m.RunStats.DisplayOutput = animateStep(m.RunStats.DisplayOutput, m.RunStats.Output)
		return m, tea.Batch(cmd1, cmd2)
	}

	var cmd tea.Cmd
	m.Input.SetHeight(maxInputHeight)
	m.Input, cmd = m.Input.Update(msg)
	m.adjustInputHeight()
	return m, cmd
}

// View renders the live area pinned at the bottom of the terminal.
// Completed content lives in terminal scrollback (printed via tea.Println).
func (m Model) View() string {
	if !m.Ready {
		return "\n  Initializing..."
	}

	var parts []string
	overlay := m.overlayView()
	appendInputArea := func() {
		if len(m.Images) > 0 {
			var tags []string
			for i := range m.Images {
				tag := fmt.Sprintf("[Image #%d]", i+1)
				if m.ImageCursor == i {
					tags = append(tags, ImageSelectedStyle.Render(tag))
				} else {
					tags = append(tags, CommandStyle.Render(tag))
				}
			}
			line := strings.Join(tags, " ")
			if m.ImageCursor >= 0 {
				line += " " + MutedStyle.Render("Delete to remove · Esc to cancel")
			} else {
				line += " " + MutedStyle.Render("(↑ to select)")
			}
			parts = append(parts, line)
		}
		parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))
		parts = append(parts, m.Input.View())
		parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))
	}

	// Welcome banner (before first message only)
	if m.ShowWelcome {
		parts = append(parts, m.renderWelcome())
	}

	// Live: streaming assistant text
	// Each block is preceded by "" for consistent blank-line spacing,
	// matching the \n prefix used in scrollback (events.go tea.Println).
	if m.IsStream {
		if thinking := strings.TrimSpace(m.Thinking.String()); thinking != "" {
			indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinking, 2)), 2)
			parts = append(parts, "", ThinkingBodyStyle.Render("● ")+strings.TrimPrefix(indented, "  "))
		}
		text := m.wrapTextForIndent(m.Streaming.String(), 2)
		indented := indentBlock(text, 2)
		parts = append(parts, "", AssistantIconStyle.Render("● ")+strings.TrimPrefix(indented, "  ")+m.Spinner.View())
	}

	// Live: pending tool execution
	for id, name := range m.PendingTools {
		line := m.ToolSpinner.View() + " " + ToolNameStyle.Render(name)
		if buf, ok := m.ToolOutputBuf[id]; ok && buf.Len() > 0 {
			output := RenderStreamingOutput(buf.String(), 8)
			line += "\n" + indentBlock(m.wrapTextForIndent(output, 2), 2)
		}
		// Show subagent thinking (all dim — thinking is secondary).
		if tbuf, ok := m.ToolThinkingBuf[id]; ok && tbuf.Len() > 0 {
			text := strings.TrimSpace(tbuf.String())
			if idx := strings.LastIndex(text, "\n"); idx >= 0 {
				text = text[idx+1:]
			}
			line += "\n" + indentBlock(ThinkingBodyStyle.Render("thinking "+truncateRunes(text, 71)), 4)
		}
		// Show accumulated streaming text.
		if dbuf, ok := m.ToolDeltaBuf[id]; ok && dbuf.Len() > 0 {
			text := strings.ReplaceAll(strings.TrimSpace(dbuf.String()), "\n", " ")
			line += "\n" + indentBlock(ReplyLabelStyle.Render("reply ")+truncateRunes(text, 74), 4)
		}
		parts = append(parts, "", line)
	}

	// Blank line before chrome
	parts = append(parts, "")

	// Run summary (shown after agent completes, cleared on next input)
	if m.ShowSummary {
		parts = append(parts, m.renderRunSummary(), "")
	}

	// Task list (persistent, non-modal display above input)
	if m.Tasks != nil && m.Tasks.Total > 0 {
		parts = append(parts, m.renderTaskList(), "")
	}

	// Interactive command overlay (e.g., /model selector) sits below the input area.
	if overlay != "" {
		if statusBar := m.RenderStatusBar(); statusBar != "" {
			parts = append(parts, statusBar, "")
		}
		appendInputArea()
		parts = append(parts, overlay)
	} else if planBar := m.RenderPlanBar(); planBar != "" {
		// Plan review / AskUser replace status bar + input area.
		parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))
		parts = append(parts, planBar)
		parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))
		// Status bar below plan review card.
		parts = append(parts, m.RenderStatusBar())
	} else if m.AskUser != nil {
		parts = append(parts, renderAskUser(m.AskUser))
	} else if m.Permission != nil {
		parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))
		parts = append(parts, renderPermission(m.Permission))
		parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))
		parts = append(parts, m.RenderStatusBar())
	} else {
		// Status bar (only shown while running or in plan mode)
		if statusBar := m.RenderStatusBar(); statusBar != "" {
			parts = append(parts, statusBar, "")
		}
		// Queued messages (sent while agent is running)
		if len(m.QueuedMsgs) > 0 {
			parts = append(parts, m.renderQueuedMsgs())
		}
		appendInputArea()
		if comp := m.renderCompletions(); comp != "" {
			parts = append(parts, comp)
		}
	}

	// Context bar (below input, hidden during ask-user)
	if !m.compActive && overlay == "" && m.AskUser == nil {
		parts = append(parts, m.RenderContextBar())
		parts = append(parts, "")
	}

	return strings.Join(parts, "\n")
}

// handleKey processes keyboard input.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ask-user UI is modal — intercepts all keys when active.
	if m.AskUser != nil {
		// Ctrl+C in AskUser: dismiss + abort (same as Esc).
		if msg.String() == "ctrl+c" {
			close(m.AskUser.respCh)
			m.AskUser = nil
			if m.Running && m.Driver != nil {
				m.Driver.Abort()
			}
			return m, nil
		}
		handled, cmd := handleAskUserKey(m.AskUser, msg)
		if handled {
			if m.AskUser.done {
				m.AskUser = nil
				// Esc dismisses AskUser and also aborts the agent run.
				if msg.String() == "esc" && m.Running && m.Driver != nil {
					m.Driver.Abort()
				}
			}
			return m, cmd
		}
	}

	// Permission prompt is modal — intercepts all keys when active.
	if m.Permission != nil {
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			m.Permission.respCh <- PermitChoiceDeny
			m.Permission = nil
			return m, nil
		}
		handled, cmd := handlePermissionKey(m.Permission, msg)
		if handled {
			if m.Permission.done {
				m.Permission = nil
			}
			return m, cmd
		}
	}

	// Interactive command overlay (e.g., /model selector).
	if m.config.Overlay != nil {
		if ov := m.config.Overlay(&m); ov != nil {
			if handled, cmd := ov.HandleKey(msg); handled {
				return m, cmd
			}
		}
	}

	// Slash command completion menu navigation.
	if m.compActive {
		switch msg.String() {
		case "tab":
			m.acceptCompletion()
			return m, nil
		case "enter":
			item := m.acceptCompletion()
			if !item.AutoExecute {
				return m, nil
			}
			// Fall through to normal enter handling to execute no-arg commands directly.
		case "up":
			if m.compIdx > 0 {
				m.compIdx--
			}
			return m, nil
		case "down":
			if m.compIdx < len(m.compItems)-1 {
				m.compIdx++
			}
			return m, nil
		case "esc":
			m.compActive = false
			return m, nil
		}
		// Other keys: fall through to textarea, then recompute completions.
	}

	// Any non-Ctrl+C key resets quit pending state.
	if m.QuitPending && msg.String() != "ctrl+c" {
		m.QuitPending = false
	}

	if m.config.OnKey != nil {
		if handled, cmd := m.config.OnKey(&m, msg); handled {
			return m, cmd
		}
	}

	// Drag-drop: detect bracketed paste containing an image file path.
	if msg.Paste && m.config.OnDrop != nil {
		if cmd := m.config.OnDrop(&m, string(msg.Runes)); cmd != nil {
			m.Pasting++
			return m, cmd
		}
	}

	// Image selection mode: navigate and delete attached images.
	if m.ImageCursor >= 0 {
		switch msg.String() {
		case "left", "h":
			if m.ImageCursor > 0 {
				m.ImageCursor--
			}
			return m, nil
		case "right", "l":
			if m.ImageCursor < len(m.Images)-1 {
				m.ImageCursor++
			}
			return m, nil
		case "delete", "backspace":
			m.Images = slices.Delete(m.Images, m.ImageCursor, m.ImageCursor+1)
			if len(m.Images) == 0 {
				m.ImageCursor = -1
			} else if m.ImageCursor >= len(m.Images) {
				m.ImageCursor = len(m.Images) - 1
			}
			return m, nil
		case "esc", "down":
			m.ImageCursor = -1
			return m, nil
		}
		// Other keys (ctrl+c, enter, etc.): exit selection, fall through to normal handling.
		m.ImageCursor = -1
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
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
			return quitResetMsg{}
		})

	case "esc":
		if m.Running && m.Driver != nil {
			m.Driver.Abort()
		}
		return m, nil

	case "alt+enter", "ctrl+j":
		m.Input.SetHeight(maxInputHeight)
		m.Input.InsertString("\n")
		m.adjustInputHeight()
		return m, nil

	case "ctrl+v":
		if m.config.OnPaste != nil {
			m.Pasting++
			return m, m.config.OnPaste(&m)
		}
		// No paste hook — fall through to textarea for text paste.
		var cmd tea.Cmd
		m.Input.SetHeight(maxInputHeight)
		m.Input, cmd = m.Input.Update(msg)
		m.adjustInputHeight()
		return m, cmd

	case "enter":
		// Block send while clipboard image read is in progress.
		if m.Pasting > 0 {
			return m, nil
		}
		text := strings.TrimSpace(m.Input.Value())
		if text == "" && len(m.Images) == 0 {
			m.Input.Reset()
			m.Input.SetHeight(1)
			return m, nil
		}
		if m.history != nil && text != "" {
			m.history.Add(text)
		}
		m.histIdx = -1
		m.histDraft = ""

		images := m.Images
		m.Images = nil
		m.ImageCursor = -1
		m.Input.Reset()
		m.Input.SetHeight(1)
		m.ShowSummary = false

		displayText := text
		if len(images) > 0 {
			var tags []string
			for i := range images {
				tags = append(tags, fmt.Sprintf("[Image #%d]", i+1))
			}
			if displayText != "" {
				displayText += " " + strings.Join(tags, " ")
			} else {
				displayText = strings.Join(tags, " ")
			}
		}

		output := m.RenderPromptOutput(displayText)
		m.ShowWelcome = false
		if m.Driver == nil {
			output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: session driver is not configured", 2)), 2)
		} else if m.Running {
			// Steer only supports text; put images back for next submission.
			m.Images = images
			m.Driver.Steer(text)
			m.QueuedMsgs = append(m.QueuedMsgs, text)
		} else if err := m.promptWithImages(text, images); err != nil {
			output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: "+err.Error(), 2)), 2)
		}
		return m, printBlock(output)

	case "up":
		// Enter image selection when cursor is on the first line of input.
		if len(m.Images) > 0 && !m.Running && m.Input.Line() == 0 {
			m.ImageCursor = len(m.Images) - 1
			return m, nil
		}
		// History navigation: cursor on first line + history available.
		if m.history != nil && m.history.Len() > 0 && m.Input.Line() == 0 && !m.Running {
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
			return m, nil
		}

	case "down":
		// History navigation: browsing history + cursor on last line.
		if m.histIdx >= 0 && m.Input.Line() == m.Input.LineCount()-1 {
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
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.Input.SetHeight(maxInputHeight) // expand before Update so repositionView uses correct YOffset
	m.Input, cmd = m.Input.Update(msg)
	m.adjustInputHeight()
	m.updateCompletions()
	return m, cmd
}

// handleResize processes terminal resize events.
func (m Model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.Width = msg.Width
	m.Height = msg.Height
	m.Ready = true
	m.Input.SetWidth(m.Width - 2)
	m.Glamour = NewGlamourRenderer(m.Width - 4)
	m.adjustInputHeight()
	if m.AskUser != nil {
		m.AskUser.width = m.Width
		m.AskUser.height = m.Height
	}
	return m, nil
}

const maxInputHeight = 8

// adjustInputHeight grows/shrinks the textarea to fit the content,
// accounting for both explicit newlines and soft-wrapping.
func (m *Model) adjustInputHeight() {
	w := m.Input.Width()
	if w <= 0 {
		w = 1
	}
	lines := 0
	for _, line := range strings.Split(m.Input.Value(), "\n") {
		// Each logical line takes at least 1 visual row, plus extra rows for wrapping.
		// Use lipgloss.Width for correct CJK / double-width character measurement.
		visualLen := lipgloss.Width(line)
		if visualLen == 0 {
			lines++
		} else {
			lines += (visualLen + w - 1) / w
		}
	}
	lines = max(lines, 1)
	lines = min(lines, maxInputHeight)
	m.Input.SetHeight(lines)
}

// handleCommandResult processes slash command results.
func (m Model) handleCommandResult(msg CommandResultMsg) (tea.Model, tea.Cmd) {
	if msg.Quit {
		return m, tea.Quit
	}
	if msg.Clear {
		m.TurnCount = 0
		m.ShowWelcome = true
		m.Images = nil
	}
	if msg.NewModel != "" {
		m.ModelName = msg.NewModel
	}
	if msg.Text != "" {
		var output string
		if m.ShowWelcome {
			output = m.renderWelcome() + "\n"
			m.ShowWelcome = false
		}
		output += indentBlock(msg.Text, 2)
		return m, printBlock(output)
	}
	return m, nil
}

// handlePrompt processes an injected prompt — renders as user message and sends to agent.
func (m Model) handlePrompt(msg PromptMsg) (tea.Model, tea.Cmd) {
	text := msg.Text
	if text == "" {
		return m, nil
	}
	m.ShowSummary = false

	output := m.RenderPromptOutput(text)
	m.ShowWelcome = false
	if m.Driver == nil {
		output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: session driver is not configured", 2)), 2)
	} else if err := m.promptWithImages(text, nil); err != nil {
		output += "\n" + indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: "+err.Error(), 2)), 2)
	}
	return m, printBlock(output)
}

// promptWithImages sends user text with optional clipboard image attachments.
// Falls back to plain text prompt when no images are present.
func (m Model) promptWithImages(text string, images []agentcore.ContentBlock) error {
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

// RenderPromptOutput renders a user message with optional welcome banner for scrollback.
func (m Model) RenderPromptOutput(text string) string {
	userLine := "\n" + m.renderUserMessage(text)
	if m.ShowWelcome {
		return m.renderWelcome() + "\n" + userLine
	}
	return userLine
}

// handleRestore replays restored session messages into terminal scrollback.
// All history is rendered as a single tea.Println to guarantee display order.
// Renders the same way as live events: user messages, thinking, tool calls/results, assistant text.
func (m Model) handleRestore(msg RestoreMsg) (tea.Model, tea.Cmd) {
	m.ShowWelcome = false

	var sb strings.Builder
	sb.WriteString(m.renderWelcome())
	sb.WriteString("\n\n")
	sb.WriteString(MutedStyle.Render("  ── restored session ──"))

	// Build a tool call ID → name index from assistant messages for pairing with tool results.
	toolCallNames := make(map[string]string) // toolCallID → tool name
	toolCallArgs := make(map[string]json.RawMessage)
	for _, am := range msg.Msgs {
		if concrete, ok := am.(agentcore.Message); ok && concrete.GetRole() == agentcore.RoleAssistant {
			for _, tc := range concrete.ToolCalls() {
				toolCallNames[tc.ID] = tc.Name
				toolCallArgs[tc.ID] = tc.Args
			}
		}
	}

	for _, am := range msg.Msgs {
		switch am.GetRole() {
		case agentcore.RoleUser:
			if text := am.TextContent(); text != "" {
				sb.WriteString("\n\n")
				sb.WriteString(m.renderUserMessage(text))
			}

		case agentcore.RoleAssistant:
			// Thinking
			if thinkingText := strings.TrimSpace(am.ThinkingContent()); thinkingText != "" {
				indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinkingText, 2)), 2)
				sb.WriteString("\n\n")
				sb.WriteString(ThinkingBodyStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
			}
			// Text content
			if content := strings.TrimSpace(am.TextContent()); content != "" {
				rendered := m.RenderMarkdown(content)
				indented := indentBlock(m.wrapTextForIndent(rendered, 2), 2)
				sb.WriteString("\n\n")
				sb.WriteString(AssistantIconStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
			}
			// Tool calls (render headers)
			if concrete, ok := am.(agentcore.Message); ok {
				for _, tc := range concrete.ToolCalls() {
					header := ToolIconStyle.Render("● ") + ToolNameStyle.Render(FormatToolHeader(tc.Name, tc.Args))
					sb.WriteString("\n")
					sb.WriteString(header)
				}
			}

		case agentcore.RoleTool:
			// Tool result — use the raw JSON content (same as live EventToolExecEnd).
			if concrete, ok := am.(agentcore.Message); ok {
				toolCallID, _ := concrete.Metadata["tool_call_id"].(string)
				isError, _ := concrete.Metadata["is_error"].(bool)
				raw := json.RawMessage(am.TextContent())
				toolName := toolCallNames[toolCallID]

				var body string
				if toolName == "edit" && !isError {
					body = indentBlock(RenderEditResult(raw), 2)
				} else if toolName == "write" && !isError {
					body = indentBlock(RenderWriteResult(raw), 2)
				} else {
					text := FormatToolResult(raw, isError)
					text = m.wrapTextForIndent(text, 4)
					if isError {
						body = indentBlock(FormatToolOutput(text, 5, ErrorStyle), 2)
					} else {
						body = indentBlock(FormatToolOutput(text, 5), 2)
					}
				}
				if body != "" {
					sb.WriteString("\n")
					sb.WriteString(body)
				}
			}
		}
	}

	sb.WriteString("\n\n")
	sb.WriteString(MutedStyle.Render("  ── end of history ──"))
	return m, tea.Println(sb.String())
}

// overlayView returns the rendered overlay content, or "" if no overlay is active.
func (m Model) overlayView() string {
	if m.config.Overlay == nil {
		return ""
	}
	ov := m.config.Overlay(&m)
	if ov == nil {
		return ""
	}
	return ov.View(m.Width)
}

// ---------------------------------------------------------------------------
// Slash command completion
// ---------------------------------------------------------------------------

// updateCompletions refreshes the completion menu based on current input.
func (m *Model) updateCompletions() {
	if m.config.Completions == nil {
		m.compActive = false
		return
	}
	text := m.Input.Value()
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t") {
		m.compActive = false
		return
	}
	prefix := text[1:]
	items := m.config.Completions(prefix)
	m.compItems = items
	m.compActive = len(items) > 0
	if m.compIdx >= len(items) {
		m.compIdx = max(len(items)-1, 0)
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
	return item
}

// renderCompletions renders the completion menu.
func (m Model) renderCompletions() string {
	if !m.compActive || len(m.compItems) == 0 {
		return ""
	}
	return m.renderCommandPalette()
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
