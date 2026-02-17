package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
)

// BlockKind identifies the type of a rendered content block.
type BlockKind int

const (
	BlockUser BlockKind = iota
	BlockAssistant
	BlockToolStart
	BlockToolEnd
	BlockThinking
	BlockError
	BlockCommand
)

// Block is a rendered content entry in the conversation history.
type Block struct {
	Kind      BlockKind
	Content   string
	Collapsed bool   // when true, only a summary line is shown
	Summary   string // one-line summary for collapsed state
}

// Config provides hooks for extending the base TUI behavior.
type Config struct {
	Placeholder string
	OnKey       func(m *Model, msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	OnEvent     func(m *Model, ev agentcore.Event) tea.Cmd
	StatusRight func(m *Model) string
	OnFooter    func(m *Model) string // optional footer line below viewport
}

// Model is the shared bubbletea Model for agent TUI applications.
type Model struct {
	Agent     *agentcore.Agent
	ModelName string

	Viewport viewport.Model
	Input    textarea.Model
	Spinner  spinner.Model

	Blocks    []Block
	Streaming *strings.Builder
	IsStream  bool

	Running       bool
	TurnCount     int
	PendingTools  map[string]string
	ToolOutputBuf map[string]*strings.Builder
	ToolOutputIdx map[string]int

	Width, Height int
	Ready         bool
	AutoScroll    bool

	ToolsCollapsed    bool // global toggle for tool output blocks
	ThinkingCollapsed bool // global toggle for thinking blocks

	Glamour *glamour.TermRenderer

	config Config
}

// New creates a Model with the given agent, model name, and optional config.
func New(agent *agentcore.Agent, modelName string, cfg ...Config) Model {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(ColorAssistant)

	ta := textarea.New()
	placeholder := "Type your message... (Enter to send, Esc to abort)"
	if c.Placeholder != "" {
		placeholder = c.Placeholder
	}
	ta.Placeholder = placeholder
	ta.Focus()
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0

	return Model{
		Agent:         agent,
		ModelName:     modelName,
		Spinner:       sp,
		Input:         ta,
		Streaming:     &strings.Builder{},
		PendingTools:  make(map[string]string),
		ToolOutputBuf: make(map[string]*strings.Builder),
		ToolOutputIdx: make(map[string]int),
		AutoScroll:    true,
		config:        c,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.Spinner.Tick, textarea.Blink)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case AgentEventMsg:
		return m.HandleAgentEvent(msg.Event)
	case CommandResultMsg:
		if msg.Quit {
			return m, tea.Quit
		}
		if msg.Clear {
			m.Blocks = nil
			m.TurnCount = 0
		}
		if msg.NewModel != "" {
			m.ModelName = msg.NewModel
		}
		if msg.Text != "" {
			m.Blocks = append(m.Blocks, Block{Kind: BlockCommand, Content: msg.Text})
		}
		m.RebuildViewport()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.Spinner, cmd = m.Spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	if m.Ready {
		var cmd tea.Cmd
		m.Viewport, cmd = m.Viewport.Update(msg)
		cmds = append(cmds, cmd)
		if !m.Viewport.AtBottom() {
			m.AutoScroll = false
		}
	}

	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View implements tea.Model.
func (m Model) View() string {
	if !m.Ready {
		return "\n  Initializing..."
	}
	footer := m.RenderFooter()
	if footer != "" {
		return fmt.Sprintf("%s\n%s\n%s\n%s", m.RenderStatusBar(), m.Viewport.View(), footer, m.Input.View())
	}
	return fmt.Sprintf("%s\n%s\n%s", m.RenderStatusBar(), m.Viewport.View(), m.Input.View())
}

// handleKey processes keyboard input, delegating to OnKey hook first.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Delegate to hook first
	if m.config.OnKey != nil {
		if handled, cmd := m.config.OnKey(&m, msg); handled {
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "ctrl+o":
		m.ToolsCollapsed = !m.ToolsCollapsed
		m.applyCollapseState()
		m.RebuildViewport()
		return m, nil

	case "ctrl+t":
		m.ThinkingCollapsed = !m.ThinkingCollapsed
		m.applyCollapseState()
		m.RebuildViewport()
		return m, nil

	case "esc":
		if m.Running {
			m.Agent.Abort()
		}
		return m, nil

	case "enter":
		text := strings.TrimSpace(m.Input.Value())
		if text == "" {
			return m, nil
		}
		m.Input.Reset()

		if m.Running {
			m.Agent.Steer(agentcore.UserMsg(text))
		} else {
			m.Blocks = append(m.Blocks, Block{
				Kind:    BlockUser,
				Content: UserPrefixStyle.Render("> You") + "\n" + text,
			})
			m.AutoScroll = true
			m.RebuildViewport()
			_ = m.Agent.Prompt(text)
		}
		return m, nil

	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.Viewport, cmd = m.Viewport.Update(msg)
		m.AutoScroll = m.Viewport.AtBottom()
		return m, cmd
	}

	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(msg)
	return m, cmd
}

// handleResize processes terminal window resize events.
func (m Model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.Width = msg.Width
	m.Height = msg.Height

	vpHeight := max(m.Height-6, 1)
	if m.config.OnFooter != nil {
		vpHeight = max(vpHeight-1, 1)
	}

	if !m.Ready {
		m.Viewport = viewport.New(m.Width, vpHeight)
		m.Viewport.MouseWheelEnabled = true
		m.Viewport.MouseWheelDelta = 3
		m.Ready = true
	} else {
		m.Viewport.Width = m.Width
		m.Viewport.Height = vpHeight
	}

	m.Input.SetWidth(m.Width - 2)
	m.Glamour = NewGlamourRenderer(m.Width - 4)
	m.RebuildViewport()
	return m, nil
}

// applyCollapseState synchronizes block Collapsed fields with global toggle state.
func (m *Model) applyCollapseState() {
	for i := range m.Blocks {
		switch m.Blocks[i].Kind {
		case BlockToolStart, BlockToolEnd:
			m.Blocks[i].Collapsed = m.ToolsCollapsed
		case BlockThinking:
			m.Blocks[i].Collapsed = m.ThinkingCollapsed
		}
	}
}
