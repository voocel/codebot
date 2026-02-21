package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
)

// Config provides hooks for extending the base TUI behavior.
type Config struct {
	Placeholder string
	Cwd         string
	GitBranch   string
	OnKey       func(m *Model, msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	OnEvent     func(m *Model, ev agentcore.Event) tea.Cmd
	StatusRight func(m *Model) string
	OnFooter    func(m *Model) string
}

// Driver defines the minimal conversation operations required by the TUI.
type Driver interface {
	Prompt(text string) error
	Steer(text string)
	Abort()
}

// runStats tracks per-run statistics displayed after agent completion.
type runStats struct {
	Turns     int
	ToolCalls int
	Input     int
	Output    int
}

// Model is the bubbletea Model for the agent TUI.
// Completed content is printed to terminal scrollback via tea.Println;
// View() only renders the live area (status + streaming + input).
type Model struct {
	Driver    Driver
	ModelName string

	Input   textarea.Model
	Spinner spinner.Model

	Streaming *strings.Builder
	Thinking  *strings.Builder
	IsStream  bool

	Running       bool
	TurnCount     int
	PendingTools  map[string]string           // toolID -> tool name
	ToolOutputBuf map[string]*strings.Builder // toolID -> streaming output

	Width int
	Ready bool

	Cwd         string
	GitBranch   string
	ShowWelcome bool
	ShowSummary bool
	RunStats    runStats

	Glamour *glamour.TermRenderer
	config  Config
}

// New creates a Model with the given agent, model name, and optional config.
func New(driver Driver, modelName string, cfg ...Config) Model {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(ColorAssistant)

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
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(ColorSeparator)
	ta.Focus()
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0

	return Model{
		Driver:        driver,
		ModelName:     modelName,
		Spinner:       sp,
		Input:         ta,
		Streaming:     &strings.Builder{},
		Thinking:      &strings.Builder{},
		PendingTools:  make(map[string]string),
		ToolOutputBuf: make(map[string]*strings.Builder),
		Cwd:           c.Cwd,
		GitBranch:     c.GitBranch,
		ShowWelcome:   true,
		config:        c,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.Spinner.Tick, textarea.Blink)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case AgentEventMsg:
		return m.HandleAgentEvent(msg.Event)
	case CommandResultMsg:
		return m.handleCommandResult(msg)
	case PromptMsg:
		return m.handlePrompt(msg)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.Spinner, cmd = m.Spinner.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(msg)
	return m, cmd
}

// View renders the live area pinned at the bottom of the terminal.
// Completed content lives in terminal scrollback (printed via tea.Println).
func (m Model) View() string {
	if !m.Ready {
		return "\n  Initializing..."
	}

	var parts []string

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
		line := m.Spinner.View() + " " + ToolNameStyle.Render(name)
		if buf, ok := m.ToolOutputBuf[id]; ok && buf.Len() > 0 {
			output := RenderStreamingOutput(buf.String(), 5)
			line += "\n" + indentBlock(m.wrapTextForIndent(output, 2), 2)
		}
		parts = append(parts, line)
	}

	// Blank line before chrome
	parts = append(parts, "")

	// Run summary (shown after agent completes, cleared on next input)
	if m.ShowSummary {
		parts = append(parts, m.renderRunSummary(), "")
	}

	// Status bar (above input)
	parts = append(parts, m.RenderStatusBar())

	// Optional footer
	if footer := m.RenderFooter(); footer != "" {
		parts = append(parts, footer)
	}

	// Separator + input + bottom border
	parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))
	parts = append(parts, m.Input.View())
	parts = append(parts, SeparatorStyle.Render(strings.Repeat("─", m.Width)))

	// Context bar (below input)
	parts = append(parts, m.RenderContextBar())
	parts = append(parts, "")

	return strings.Join(parts, "\n")
}

// handleKey processes keyboard input.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.config.OnKey != nil {
		if handled, cmd := m.config.OnKey(&m, msg); handled {
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		if m.Running && m.Driver != nil {
			m.Driver.Abort()
		}
		return m, nil

	case "alt+enter", "ctrl+j":
		m.Input.InsertString("\n")
		m.adjustInputHeight()
		return m, nil

	case "enter":
		text := strings.TrimSpace(m.Input.Value())
		if text == "" {
			m.Input.Reset()
			m.Input.SetHeight(1)
			return m, nil
		}
		m.Input.Reset()
		m.Input.SetHeight(1)
		m.ShowSummary = false

		userLine := "\n" + m.renderUserMessage(text)

		// Persist welcome to scrollback before it leaves the live area.
		var output string
		if m.ShowWelcome {
			output = m.renderWelcome() + "\n" + userLine
			m.ShowWelcome = false
		} else {
			output = userLine
		}

		if m.Driver == nil {
			output += "\n" + ErrorStyle.Render("  error: session driver is not configured")
		} else if m.Running {
			m.Driver.Steer(text)
		} else if err := m.Driver.Prompt(text); err != nil {
			output += "\n" + ErrorStyle.Render("  error: "+err.Error())
		}
		return m, tea.Println(output)
	}

	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(msg)
	return m, cmd
}

// handleResize processes terminal resize events.
func (m Model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.Width = msg.Width
	m.Ready = true
	m.Input.SetWidth(m.Width - 2)
	m.Glamour = NewGlamourRenderer(m.Width - 4)
	return m, nil
}

const maxInputHeight = 8

// adjustInputHeight grows/shrinks the textarea to fit the content lines.
func (m *Model) adjustInputHeight() {
	lines := strings.Count(m.Input.Value(), "\n") + 1
	if lines > maxInputHeight {
		lines = maxInputHeight
	}
	if lines < 1 {
		lines = 1
	}
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
		return m, tea.Println(output)
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

	userLine := "\n" + m.renderUserMessage(text)

	var output string
	if m.ShowWelcome {
		output = m.renderWelcome() + "\n" + userLine
		m.ShowWelcome = false
	} else {
		output = userLine
	}

	if m.Driver == nil {
		output += "\n" + ErrorStyle.Render("  error: session driver is not configured")
	} else if err := m.Driver.Prompt(text); err != nil {
		output += "\n" + ErrorStyle.Render("  error: "+err.Error())
	}
	return m, tea.Println(output)
}

