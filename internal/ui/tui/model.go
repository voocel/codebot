package tui

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
)

// Commands runs slash commands; package commands implements it.
type Commands interface {
	// Run runs a "/name args" line.
	Run(line string) tea.Cmd
	// Complete lists the commands whose names start with prefix.
	Complete(prefix string) []CompletionItem
	// Overlay is the interactive command on screen, or nil.
	Overlay() *OverlayState
}

// CompletionItem is a single command completion candidate.
type CompletionItem struct {
	Name        string // command name without "/" (e.g. "model")
	Description string
	Kind        string
	Aliases     []string
	AutoExecute bool
}

// OverlayState bridges an interactive command overlay to the TUI. It
// replaces the input area while open.
type OverlayState struct {
	HandleKey func(msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	View      func(width, height int) string
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
	app      *app.App
	commands Commands
	version  string

	Input   textarea.Model
	Spinner spinner.Model

	ToolSpinner spinner.Model // breathing-dot spinner for tool execution

	Streaming *strings.Builder
	Thinking  *strings.Builder
	IsStream  bool

	// conv is the open conversation, agents and tasks its background work.
	conv   *app.Conversation
	agents *app.AgentHub
	tasks  *task.Runtime
	// restored is the history to replay into scrollback once the terminal's
	// size is known.
	restored []agentcore.Message

	Mode interact.Mode // permission mode, shown in the context bar
	// Status is the conversation's latest: its model, usage and the like.
	Status session.Status

	Running         bool
	PendingTools    map[string]string           // toolID -> display label
	HiddenToolCalls map[string]struct{}         // toolID -> internal call hidden from UI
	ToolHeaders     map[string]string           // toolID -> formatted header (printed at end)
	ToolOutputBuf   map[string]*strings.Builder // toolID -> streaming output
	SubagentUsage   map[string]*subagentUsage   // toolID -> what its sub-agent runs used

	Width  int
	Height int
	Ready  bool

	Cwd         string
	GitBranch   string
	ShowWelcome bool
	RunStats    runStats
	Images      []litellm.Block // attached images (from Ctrl+V clipboard paste)
	ImageCursor int             // -1 = not selecting; 0+ = selected image index
	Pasting     int             // number of async image reads in progress (clipboard paste or drag-drop)
	// Pasted holds oversized paste bodies keyed by the id in their reference.
	// Kept for the whole session, not cleared on submit, so a prompt recalled
	// from history still expands. See paste.go.
	Pasted      map[int]string
	nextPasteID int

	Markdown *markdown.Renderer

	Dialogs dialogQueue // modal "waiting on user" cards: permission / ask_user

	Todos []todo.Item // current todo_write list; displayed above input

	todoHideVersion uint64

	QueuedMsgs []string // messages queued while agent is running (display only)

	// StatusDeadline enables an optional countdown.
	StatusPrefix   string
	StatusDeadline time.Time

	// Scrollback mirrors the stream of pre-formatted bodies sent to
	// tea.Println. It exists solely to cure the terminal-resize ghost /
	// reflow duplication problem.
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

	history   *inputHistory // input history store
	histIdx   int           // -1 = not browsing; 0+ = current position (0 = most recent)
	histDraft string        // stashed input before history navigation

	// TranscriptModal renders the live transcript of a sub-agent when the
	// user opens the popup (Ctrl+O). nil = closed; non-nil = open and
	// full-screen, taking over all keyboard input except Esc / Ctrl+O /
	// Ctrl+C and the scroll keys.
	TranscriptModal *TranscriptView
	// TranscriptAgent is the agent currently shown in the modal.
	TranscriptAgent string
	// transcriptUnsubscribe drops the hub subscription that feeds the
	// modal; nil when no modal is open.
	transcriptUnsubscribe func()

	// FleetFocus is true when keyboard focus has moved from the input into
	// the live agent list pinned below it (entered by pressing ↓ at the last
	// input line). While true, navigation keys drive FleetCursor instead of
	// the textarea. FleetCursor indexes the sorted fleet agent list.
	FleetFocus  bool
	FleetCursor int
}

// New creates the TUI for a's open conversation.
func New(a *app.App, cmds Commands, version string) *Model {
	m := newModel()
	m.app, m.commands, m.version = a, cmds, version
	m.Mode = a.Mode()
	m.open(a.Current())
	return m
}

// open shows conv: its status, background work, and history to replay.
func (m *Model) open(conv *app.Conversation) {
	m.conv, m.agents, m.tasks = conv, conv.Agents(), conv.Tasks()
	m.history = newInputHistory(filepath.Join(config.UserConfigDir(), "history.jsonl"), m.app.Cwd(), conv.ID())
	m.histIdx = -1
	m.applyStatus(conv.Status())
	m.GitBranch = conv.GitBranch()
	m.restored = conv.History()
}

// applyStatus takes in the conversation's status.
func (m *Model) applyStatus(cs app.Status) {
	m.Status, m.Cwd = cs.Status, cs.Cwd
}

// newModel creates a Model bound to nothing.
func newModel() *Model {
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
	ta.Placeholder = defaultPlaceholder
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
		Spinner:         sp,
		ToolSpinner:     tsp,
		Input:           ta,
		Streaming:       &strings.Builder{},
		Thinking:        &strings.Builder{},
		PendingTools:    make(map[string]string),
		HiddenToolCalls: make(map[string]struct{}),
		ToolHeaders:     make(map[string]string),
		ToolOutputBuf:   make(map[string]*strings.Builder),
		SubagentUsage:   make(map[string]*subagentUsage),
		ShowWelcome:     true,
		ImageCursor:     -1,
		Markdown:        markdown.NewRenderer(80),
		histIdx:         -1,
	}
}
