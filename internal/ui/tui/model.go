package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
)

// PlanBarInfo provides plan mode status for the status bar.
type PlanBarInfo struct {
	Tag     string   // appended to status bar (e.g., "plan mode")
	Choices []string // selectable options
	Active  int      // active choice index

	// Plan review card fields. The plan body is already in scrollback; this
	// card only shows the title, constraints, choices, and edit path.
	Title        string
	PlanFilePath string
	Details      []string
	OtherMode    bool   // typing custom feedback
	OtherBuf     string // custom feedback buffer
}

// Config provides hooks for extending the base TUI behavior.
type Config struct {
	Placeholder          string
	Version              string
	Provider             string
	ContextWindow        int
	Cwd                  string
	PlansDir             string // absolute path to the plan files directory; enables cc-style hidden rendering for write/edit on plan files
	GitBranch            string
	EnvHint              string                   // shown below welcome when using env var credentials
	History              *storage.History         // input history (Up/Down navigation)
	InitialTasks         *storage.TaskSnapshot    // initial task snapshot restored before first render
	RestoredMessages     []agentcore.AgentMessage // messages restored from a previous session (rendered on Init)
	OnKey                func(m *Model, msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	OnEvent              func(m *Model, ev agentcore.Event) tea.Cmd
	OnPaste              func(m *Model) tea.Cmd              // Ctrl+V: read clipboard image, return ImageAttachedMsg
	OnDrop               func(m *Model, text string) tea.Cmd // Drag-drop: if text is image path, return cmd; else nil
	OnMCPReady           func(msg MCPReadyMsg)               // called when MCP servers finish connecting
	OnHideCompletedTasks func(snap storage.TaskSnapshot) tea.Cmd
	StatusRight          func(m *Model) string
	StatusMode           func(m *Model) string // mode indicator for context bar (e.g. "⏵⏵ trust")
	StatusPlan           func(m *Model) *PlanBarInfo
	Overlay              func(m *Model) *OverlayState         // interactive command overlay
	Completions          func(prefix string) []CompletionItem // slash command completions
	OnBtwResult          func(msg BtwResultMsg)               // called when /btw side question completes
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
	HandleKey     func(msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	View          func(width, height int) string
	ReplacesInput bool // when true, overlay replaces the input area instead of appearing below it
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

// Deps holds external dependencies and static configuration for the TUI.
type Deps struct {
	Driver        Driver
	ModelName     string
	ContextWindow int
	Provider      string
	Version       string
	config        Config
}

// State holds mutable runtime state for the TUI.
type State struct {
	Input   textarea.Model
	Spinner spinner.Model

	ToolSpinner spinner.Model // breathing-dot spinner for tool execution

	Streaming *strings.Builder
	Thinking  *strings.Builder
	IsStream  bool
	// SuppressNextAssistantText avoids double-printing when an in-flight
	// assistant stream is flushed manually before a terminal tool aborts the
	// run, but a late MessageEnd still arrives with the same content.
	SuppressNextAssistantText string

	Running         bool
	TurnCount       int
	PendingTools    map[string]string           // toolID -> display label (== "Plan" marks a plan-file write/edit)
	HiddenToolCalls map[string]struct{}         // toolID -> internal call hidden from UI
	ToolHeaders     map[string]string           // toolID -> formatted header (printed at end)
	ToolOutputBuf   map[string]*strings.Builder // toolID -> streaming output
	ToolDeltaBuf    map[string]*strings.Builder // toolID -> accumulated subagent delta text
	ToolThinkingBuf map[string]*strings.Builder // toolID -> accumulated subagent thinking text

	Width  int
	Height int
	Ready  bool

	Cwd         string
	PlansDir    string
	GitBranch   string
	ShowWelcome bool
	EnvHint     string // env var hint shown below welcome
	RunStats    runStats
	Images      []agentcore.ContentBlock // attached images (from Ctrl+V clipboard paste)
	ImageCursor int                      // -1 = not selecting; 0+ = selected image index
	Pasting     int                      // number of async image reads in progress (clipboard paste or drag-drop)

	Markdown *markdown.Renderer

	AskUser    *askUserState    // non-nil when ask-user UI is active
	Permission *permissionState // non-nil when permission prompt is active

	Tasks *storage.TaskSnapshot // non-nil when task items exist; displayed above input

	taskHideVersion uint64

	QueuedMsgs []string // messages queued while agent is running (display only)

	// Retry countdown shown in the live area while auto-retrying.
	// RetryPrefix is the static text (e.g. "Request failed, retrying (1/3)");
	// RetryDeadline is when the retry will fire — View() computes remaining seconds.
	RetryPrefix   string
	RetryDeadline time.Time

	MCPLoading bool // true while MCP servers are connecting in background

	// Scrollback mirrors the stream of pre-formatted bodies sent to
	// tea.Println. It exists solely to cure the terminal-resize ghost /
	// reflow duplication problem (重影).
	//
	// Precise cause: Println content, once written, is inert — bubbletea
	// never redraws it, so reflow at worst re-wraps it in place without
	// duplicating. The ghosts come exclusively from the live View()
	// (status bar, input panel borders, streaming area). On resize the
	// terminal pushes those rows up into OS scrollback to make room for
	// the new viewport, but bubbletea's `linesRendered` cursor tracking
	// still points at the old position — its next frame's "erase previous
	// frame" sequence misses, and the pushed-up copy is marooned in
	// scrollback as a ghost.
	//
	// Fix: on WindowSizeMsg we wipe viewport + OS scrollback (`\x1b[2J`
	// + `\x1b[3J` + `\x1b[H`) to evict the ghosts, then replay this cache
	// so legitimate Println history survives the nuke. Without the cache
	// we'd be trading ghosts for lost conversation history. See
	// handleResize for the replay path, Emit for the write path,
	// handleCommandResult (msg.Clear) for the reset path.
	//
	// Entries are the exact string passed to tea.Println (as returned by
	// formatScrollbackBlock), so joining them with "\n" and Println'ing
	// once is byte-for-byte equivalent to the original per-block Println
	// sequence — tea.Println splits on "\n" internally. Bounded by
	// scrollbackCacheLimit; entries beyond the cap are dropped FIFO,
	// which only materialises as lost history after a resize (the live
	// terminal scrollback remains complete until the next resize clears
	// it).
	Scrollback []string

	Suggestion string // prompt suggestion shown as placeholder after agent completes

	compItems    []CompletionItem // current completion candidates
	compIdx      int              // selected completion index
	compActive   bool             // completion menu visible
	cmdHighlight string           // recognized command to highlight (e.g. "/btw")

	QuitPending bool // true after first Ctrl+C, waiting for second to quit

	history   *storage.History // input history store (nil = disabled)
	histIdx   int              // -1 = not browsing; 0+ = current position (0 = most recent)
	histDraft string           // stashed input before history navigation
}

// Model is the bubbletea Model for the agent TUI.
// Completed content is printed to terminal scrollback via tea.Println;
// View() only renders the live area (status + streaming + input).
type Model struct {
	Deps
	State
}

// New creates a Model with the given agent, model name, and optional config.
func New(driver Driver, modelName string, cfg ...Config) *Model {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}

	var initialTasks *storage.TaskSnapshot
	var taskHideVersion uint64
	if c.InitialTasks != nil && c.InitialTasks.Total > 0 {
		snap := *c.InitialTasks
		initialTasks = &snap
		if tasksFullyCompleted(snap) {
			taskHideVersion = 1
		}
	}

	sp := spinner.New()
	sp.Spinner = spinner.Spinner{
		Frames: []string{"·", "✢", "✶", "✽", "✶", "✢", "·"},
		FPS:    time.Second / 30, // 30fps for smooth shimmer
	}
	sp.Style = lipgloss.NewStyle().Foreground(Live)

	tsp := spinner.New()
	tsp.Spinner = spinner.Spinner{
		Frames: []string{"●", "◉", "○", "◉"},
		FPS:    time.Second / 4,
	}
	tsp.Style = lipgloss.NewStyle().Foreground(Accent)

	ta := textarea.New()
	placeholder := defaultPlaceholder
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
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(InputRule)
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(Subtle)
	ta.BlurredStyle.Prompt = lipgloss.NewStyle().Foreground(InputRule)
	ta.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(Subtle)
	ta.Focus()
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0

	return &Model{
		Deps: Deps{
			Driver:        driver,
			ModelName:     modelName,
			ContextWindow: c.ContextWindow,
			Provider:      c.Provider,
			Version:       c.Version,
			config:        c,
		},
		State: State{
			Spinner:         sp,
			ToolSpinner:     tsp,
			Input:           ta,
			Streaming:       &strings.Builder{},
			Thinking:        &strings.Builder{},
			PendingTools:    make(map[string]string),
			HiddenToolCalls: make(map[string]struct{}),
			ToolHeaders:     make(map[string]string),
			ToolOutputBuf:   make(map[string]*strings.Builder),
			ToolDeltaBuf:    make(map[string]*strings.Builder),
			ToolThinkingBuf: make(map[string]*strings.Builder),
			Cwd:             c.Cwd,
			PlansDir:        c.PlansDir,
			GitBranch:       c.GitBranch,
			ShowWelcome:     true,
			EnvHint:         c.EnvHint,
			ImageCursor:     -1,
			Markdown:        markdown.NewRenderer(80),
			Tasks:           initialTasks,
			taskHideVersion: taskHideVersion,
			history:         c.History,
			histIdx:         -1,
		},
	}
}
