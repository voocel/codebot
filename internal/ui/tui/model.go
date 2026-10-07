package tui

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/tui/commands"
	"github.com/voocel/codebot/internal/ui/tui/editor"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// Model shows the conversation (or a page over it) on top, and the editor
// (or the top panel) below.
type Model struct {
	app     *app.App
	cmds    *commands.Registry
	version string

	conv   *app.Conversation
	status app.Status
	mode   interact.Mode
	branch string
	// asked is the folder surface the user was already prompted about this
	// session; it is not prompted again.
	asked app.Surface

	width, height int
	// mainTop and mainHeight locate the main area in the last view, for
	// mouse hit-testing.
	mainTop, mainHeight int

	t      *transcript.Transcript
	chat   *chatView
	page   *page
	editor *editor.Editor
	panels []panel.Panel

	// Requests pop up unprompted, possibly while the user types. The top
	// request takes keys only once armDelay has passed since shownAt and
	// since the last key; armed is the request that passed that check.
	shown   panel.Panel
	shownAt time.Time
	armed   panel.Panel
	lastKey time.Time

	run      run
	shell    *shellRun // the "!" line running, nil when none
	pending  []pending
	nextID   int
	stopped  bool // the user stopped the run, which drops the pending inputs
	expanded bool

	// Welcome screen state; recentAt maps a main-area line to the session
	// shown on it.
	recent   []app.SessionInfo
	recentAt map[int]string
	tip      string

	ticking   bool
	notice    string // shown as the first toast
	toast     string
	toastID   int
	quitArmed bool
	away      bool // the terminal reported losing focus
}

// pending is an input sent but not yet in the conversation.
type pending struct {
	id     int
	text   string
	posted bool
}

type shellRun struct {
	line    string
	started time.Time
	cancel  context.CancelFunc
}

// page shows a sub-agent's run over the conversation.
type page struct {
	title  string
	view   *chatView
	live   func() bool // whether what it shows still runs
	t      *transcript.Transcript
	events <-chan agentcore.Event // a background sub-agent's
	stop   func()
}

type (
	agentMsg      struct{ ev agentcore.Event }
	runStartedMsg struct{}
	idleMsg       struct{}
	statusMsg     struct {
		conv   *app.Conversation
		status app.Status
	}
	sessionErrMsg struct{ err error }
	openedMsg     struct{ conv *app.Conversation }
	modeMsg       struct{ mode interact.Mode }
	connectedMsg  struct{ report app.MCPReport }
	updatesMsg    struct{ plugins []string }
	reloadedMsg   struct{}
	suggestionMsg struct {
		conv *app.Conversation
		text string
	}
	approveMsg struct {
		req   interact.Approval
		reply chan<- interact.Verdict
	}
	askMsg struct {
		qs    []interact.Question
		reply chan<- interact.Answers
	}
	withdrawMsg  struct{ key any }
	submittedMsg struct {
		id  int
		err error
	}
	tickMsg      struct{}
	toastEndMsg  struct{ id int }
	disarmMsg    struct{}
	shellDoneMsg struct{ cells []transcript.Cell }
	agentPageMsg struct {
		p  *page
		ev agentcore.Event
		ok bool
	}
)

func newModel(a *app.App, version string) *Model {
	m := &Model{app: a, version: version, cmds: commands.New(a, version), mode: a.Mode(), tip: randomTip()}
	m.editor = editor.New(m.commands, func() string { return m.conv.Cwd() })
	m.editor.SetHistory(editor.NewHistory(filepath.Join(config.UserConfigDir(), "history.jsonl"), a.Cwd()))
	m.open(a.Current())
	m.askTrust()
	return m
}

// askTrust shows the trust panel, replacing an earlier one, when the folder
// has surface the user was not yet asked about this session.
func (m *Model) askTrust() tea.Cmd {
	ask := m.app.Trust().Ask()
	if len(ask) == 0 {
		m.remove(commands.IsAsk)
	}
	if len(ask.Missing(m.asked)) == 0 {
		return nil
	}
	m.asked = m.asked.With(ask...)
	m.remove(commands.IsAsk)
	return m.push(commands.TrustPanel(m.app, false))
}

func (m *Model) open(conv *app.Conversation) {
	m.conv = conv
	m.status = conv.Status()
	m.branch = conv.GitBranch()
	m.t = transcript.Load(conv.History())
	m.chat = newChatView(func() []transcript.Cell { return m.t.Cells() }, m.welcome)
	m.closePage()
	m.panels = nil
	m.pending, m.stopped = nil, false
	m.run = run{todos: todo.FromHistory(conv.History())}
	m.editor.SetSession(conv.ID())
	m.editor.SetSuggestion("")
	m.editor.SetAccent(modeColor(m.mode))
}

func (m *Model) commands() []editor.Completion {
	var out []editor.Completion
	for _, c := range m.cmds.Commands() {
		// A command whose arguments are optional runs as picked.
		run := c.Args == "" || strings.HasPrefix(c.Args, "[")
		out = append(out, editor.Completion{Name: c.Name, Aliases: c.Aliases, Description: c.Description, Run: run})
	}
	return out
}

func (m *Model) Init() tea.Cmd {
	var notice tea.Cmd
	if m.notice != "" {
		notice = m.notify(m.notice)
	}
	return tea.Batch(m.loadRecent(), notice)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, m.update(msg)
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.editor.SetWidth(m.width)
		return nil
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseMsg:
		return m.mouse(msg)
	case tea.FocusMsg:
		m.away = false
		return nil
	case tea.BlurMsg:
		m.away = true
		return nil
	case tea.PasteMsg:
		if p := m.top(); p != nil {
			return m.updatePanel(p, msg)
		}
		cmd, _ := m.editor.Update(msg)
		return cmd

	case agentMsg:
		return m.agent(msg.ev)
	case runStartedMsg:
		m.run.start(time.Now())
		m.editor.SetSuggestion("")
		return m.tick()
	case idleMsg:
		m.idle()
		return m.alert("Done")
	case statusMsg:
		if msg.conv == m.conv {
			m.status = msg.status
		}
		return nil
	case sessionErrMsg:
		m.t.Append(transcript.Fail(app.ErrorText(msg.err)))
		return nil
	case openedMsg:
		m.open(msg.conv)
		return m.loadRecent()
	case recentMsg:
		m.setRecent(msg.sessions)
		return nil
	case modeMsg:
		m.mode = msg.mode
		m.editor.SetAccent(modeColor(m.mode))
		return nil
	case reloadedMsg:
		return m.askTrust()
	case connectedMsg:
		for _, e := range msg.report.Errors {
			m.t.Append(transcript.Fail("MCP: " + e))
		}
		for _, name := range msg.report.Login {
			m.t.Append(transcript.Note("MCP: " + name + " needs you to log in · /mcp login " + name))
		}
		if p := commands.PendingPlugins(m.app); p != "" {
			m.t.Append(transcript.Note(p))
		}
		if n := msg.report.Tools; n > 0 {
			return m.notify("MCP connected · " + strconv.Itoa(n) + " tools")
		}
		return nil
	case updatesMsg:
		if len(msg.plugins) > 0 {
			m.t.Append(transcript.Note("Updates for " + strings.Join(msg.plugins, ", ") + " · /plugins update"))
		}
		return nil
	case suggestionMsg:
		if msg.conv == m.conv && !m.run.active && m.editor.Empty() {
			m.editor.SetSuggestion(msg.text)
		}
		return nil

	case approveMsg:
		p := panel.NewPermission(msg.req, msg.reply, func() { m.app.SetMode(interact.ModeAcceptEdits) })
		return tea.Batch(m.push(p), m.alert("Allow "+transcript.Title(msg.req.Tool)+"?"))
	case askMsg:
		return tea.Batch(m.push(panel.NewAsk(msg.qs, msg.reply)), m.alert("A question for you"))
	case withdrawMsg:
		m.remove(func(p panel.Panel) bool {
			r, ok := p.(panel.Request)
			return ok && r.Key() == msg.key
		})
		return nil

	case submittedMsg:
		i := slices.IndexFunc(m.pending, func(p pending) bool { return p.id == msg.id })
		if i < 0 {
			return nil
		}
		if msg.err != nil {
			m.pending = slices.Delete(m.pending, i, i+1)
			m.t.Append(transcript.Fail(app.ErrorText(msg.err)))
			return nil
		}
		m.pending[i].posted = true
		return nil

	case tickMsg:
		m.ticking = false
		m.run.roll(time.Now())
		return m.tick()
	case toastEndMsg:
		if msg.id == m.toastID {
			m.toast = ""
		}
		return nil
	case disarmMsg:
		m.quitArmed = false
		return nil
	case shellDoneMsg:
		m.shell = nil
		for _, c := range msg.cells {
			m.t.Append(c)
		}
		return nil
	case agentPageMsg:
		if msg.p != m.page {
			return nil
		}
		if !msg.ok {
			return nil
		}
		m.page.t.Apply(msg.ev)
		return waitAgent(m.page)

	case transcript.Cell:
		m.t.Append(msg)
		return nil
	case panel.Panel:
		return m.push(msg)
	case commands.OpenAgent:
		return m.openAgent(msg.Name)
	case commands.Copy:
		return m.copy(msg.Text)
	case editor.Error:
		m.t.Append(transcript.Fail(msg.Err.Error()))
		return nil
	}

	// Other messages go to every panel, which may have work in flight, and
	// to the editor.
	var cmds []tea.Cmd
	for _, p := range slices.Clone(m.panels) {
		cmds = append(cmds, m.updatePanel(p, msg))
	}
	cmd, _ := m.editor.Update(msg)
	return tea.Batch(append(cmds, cmd)...)
}

func (m *Model) agent(ev agentcore.Event) tea.Cmd {
	now := time.Now()
	m.t.Apply(ev)
	m.run.apply(ev, now)
	switch e := ev.(type) {
	case agentcore.MessageEnd:
		// A sent input joined the conversation. Match by text, not order:
		// inputs can overtake each other, and a skill's message is not one.
		if text, ok := app.UserText(e.Message); ok && e.Message.Role == litellm.RoleUser {
			if i := slices.IndexFunc(m.pending, func(p pending) bool { return p.text == text }); i >= 0 {
				m.pending = slices.Delete(m.pending, i, i+1)
			}
		}
	case agentcore.RunEnd:
		if s := m.run.summary(now); s != "" {
			m.t.Append(&transcript.Notice{Level: transcript.Summary, Text: s})
		}
		m.run.active = false
	case agentcore.CompactionStart:
		return m.tick()
	}
	return nil
}

// idle puts the inputs a stop dropped back in the editor.
func (m *Model) idle() {
	m.run.active = false
	var dropped []string
	m.pending = slices.DeleteFunc(m.pending, func(p pending) bool {
		if p.posted {
			dropped = append(dropped, p.text)
		}
		return p.posted
	})
	if m.stopped && len(dropped) > 0 {
		m.editor.Insert(strings.Join(dropped, "\n\n"))
	}
	m.stopped = false
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	s, now := k.String(), time.Now()
	defer func() { m.lastKey = now }()
	if s != "ctrl+c" {
		m.quitArmed = false
	}
	switch s {
	case "ctrl+o":
		m.expanded = !m.expanded
		return nil
	case "ctrl+t":
		return m.pager()
	case "shift+up":
		m.main().scroll(-1)
		return nil
	case "shift+down":
		m.main().scroll(1)
		return nil
	}

	// Keys typed just as a request appears go to the editor, not to it.
	if p := m.top(); p != nil && m.ready(p, now) {
		return m.updatePanel(p, k)
	}
	switch s {
	case "pgup":
		m.main().scroll(-max(m.mainHeight-2, 1))
		return nil
	case "pgdown":
		m.main().scroll(max(m.mainHeight-2, 1))
		return nil
	}
	if m.page != nil {
		switch s {
		case "esc", "q", "ctrl+c":
			m.closePage()
		case "up", "k":
			m.page.view.scroll(-1)
		case "down", "j":
			m.page.view.scroll(1)
		case "home", "g":
			m.page.view.toTop()
		case "end", "G":
			m.page.view.toBottom()
		}
		return nil
	}

	switch s {
	case "ctrl+c":
		return m.interrupt()
	case "esc":
		switch {
		case m.editor.Dismiss():
		case m.chat.sel.on:
			m.chat.clearSelection()
		case m.run.active:
			m.stop()
		case m.shell != nil:
			m.shell.cancel()
		}
		return nil
	case "shift+tab":
		i := slices.Index(interact.Modes, m.mode)
		m.app.SetMode(interact.Modes[(i+1)%len(interact.Modes)])
		return nil
	case "home", "end":
		if m.editor.Empty() {
			if s == "home" {
				m.chat.toTop()
			} else {
				m.chat.toBottom()
			}
			return nil
		}
	}
	cmd, in := m.editor.Update(k)
	if in != nil {
		return tea.Batch(cmd, m.send(*in))
	}
	return cmd
}

// interrupt handles ctrl+c: clear the input, else stop the run or the shell
// line, else quit on a second press.
func (m *Model) interrupt() tea.Cmd {
	switch {
	case !m.editor.Empty():
		m.editor.Clear()
		return nil
	case m.run.active:
		m.stop()
		return nil
	case m.shell != nil:
		m.shell.cancel()
		return nil
	case m.quitArmed:
		return tea.Quit
	}
	m.quitArmed = true
	return tea.Batch(m.notify("Press ctrl+c again to quit"), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return disarmMsg{} }))
}

func (m *Model) stop() {
	m.stopped = true
	m.conv.Cancel()
}

func (m *Model) send(in editor.Input) tea.Cmd {
	m.chat.toBottom()
	text := strings.TrimSpace(in.Text)
	if len(in.Images) == 0 {
		if commands.IsCommand(text) {
			return m.cmds.Run(text)
		}
		if line, ok := strings.CutPrefix(text, "!"); ok && strings.TrimSpace(line) != "" {
			if m.shell != nil {
				m.editor.Insert(in.Text)
				return m.notify("A shell command is running · esc stops it")
			}
			return m.runShell(strings.TrimSpace(line))
		}
	}
	if text == "" {
		text = "Describe the image."
	}
	m.nextID++
	id := m.nextID
	m.pending = append(m.pending, pending{id: id, text: text})
	blocks := append([]litellm.Block{litellm.Text(text)}, in.Images...)
	conv := m.conv
	// Submit runs the UserPromptSubmit hooks, so keep it off the TUI
	// goroutine.
	return func() tea.Msg {
		return submittedMsg{id, conv.Submit(context.Background(), blocks)}
	}
}

// runShell shows the output to the user but not to the agent.
func (m *Model) runShell(line string) tea.Cmd {
	m.t.Append(&transcript.Prompt{Text: line, Kind: transcript.ToShell})
	ctx, cancel := context.WithCancel(context.Background())
	m.shell = &shellRun{line: line, started: time.Now(), cancel: cancel}
	dir := m.conv.Cwd()
	run := func() tea.Msg {
		defer cancel()
		cmd := shellCommand(ctx, line)
		cmd.Dir = dir
		cmd.WaitDelay = time.Second // don't hang on pipes held by leftover children
		out, err := cmd.CombinedOutput()
		var cells []transcript.Cell
		if text := strings.TrimRight(string(out), "\n"); text != "" {
			cells = append(cells, transcript.Print(text))
		}
		switch {
		case ctx.Err() != nil:
			cells = append(cells, transcript.Note("Stopped"))
		case err != nil:
			cells = append(cells, transcript.Fail(err.Error()))
		case len(cells) == 0:
			cells = append(cells, transcript.Note("(no output)"))
		}
		return shellDoneMsg{cells}
	}
	return tea.Batch(run, m.tick())
}

func (m *Model) top() panel.Panel {
	if len(m.panels) == 0 {
		return nil
	}
	return m.panels[len(m.panels)-1]
}

// push puts p on top, except that a request goes below earlier requests so
// requests are answered in arrival order.
func (m *Model) push(p panel.Panel) tea.Cmd {
	at := len(m.panels)
	if isRequest(p) {
		if i := slices.IndexFunc(m.panels, isRequest); i >= 0 {
			at = i
		}
	}
	m.panels = slices.Insert(m.panels, at, p)
	m.restack()
	if i, ok := p.(panel.Initer); ok {
		return i.Init()
	}
	return nil
}

func (m *Model) remove(match func(panel.Panel) bool) {
	m.panels = slices.DeleteFunc(m.panels, match)
	m.restack()
}

// restack tells the top request how many wait behind it, and restarts the
// arm delay when a new panel reaches the top.
func (m *Model) restack() {
	top := m.top()
	if top != m.shown {
		m.shown, m.shownAt = top, time.Now()
	}
	if r, ok := top.(panel.Request); ok {
		behind := -1
		for _, p := range m.panels {
			if isRequest(p) {
				behind++
			}
		}
		r.Queue(behind)
	}
}

func isRequest(p panel.Panel) bool {
	_, ok := p.(panel.Request)
	return ok
}

// armDelay keeps keys meant for the editor from answering a request that
// just appeared.
var armDelay = 500 * time.Millisecond

func (m *Model) ready(p panel.Panel, now time.Time) bool {
	if !isRequest(p) || p == m.armed {
		return true
	}
	if now.Sub(m.shownAt) < armDelay || now.Sub(m.lastKey) < armDelay {
		return false
	}
	m.armed = p
	return true
}

func (m *Model) updatePanel(p panel.Panel, msg tea.Msg) tea.Cmd {
	cmd, done := p.Update(msg)
	if done {
		m.remove(func(q panel.Panel) bool { return q == p })
	}
	return cmd
}

// main is the view the scroll keys move.
func (m *Model) main() *chatView {
	if m.page != nil {
		return m.page.view
	}
	return m.chat
}

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	ms := msg.Mouse()
	v := m.main()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		switch ms.Button {
		case tea.MouseWheelUp:
			v.scroll(-3)
		case tea.MouseWheelDown:
			v.scroll(3)
		}
	case tea.MouseClickMsg:
		y := ms.Y - m.mainTop
		if id, ok := m.recentAt[y]; ok && ms.Button == tea.MouseLeft && v == m.chat && len(m.t.Cells()) == 0 && !m.run.active {
			return commands.Open(m.app, id)
		}
		if ms.Button == tea.MouseLeft && y >= 0 && y < m.mainHeight {
			v.press(ms.X-1, y)
		}
	case tea.MouseMotionMsg:
		if ms.Button == tea.MouseLeft {
			v.drag(ms.X-1, ms.Y-m.mainTop)
		}
	case tea.MouseReleaseMsg:
		if !v.sel.dragging {
			return nil
		}
		p := v.sel.from.pos
		if text := v.release(); text != "" {
			return m.copy(text)
		}
		return m.click(v, p)
	}
	return nil
}

// click opens a sub-agent call's runs and toggles other cells.
func (m *Model) click(v *chatView, p pos) tea.Cmd {
	c := v.cell(p)
	if t, ok := c.(*transcript.Tool); ok && len(t.Agents()) > 0 && v == m.chat {
		m.openCall(t)
		return nil
	}
	if t, ok := c.(transcript.Toggler); ok {
		t.Toggle()
	}
	return nil
}

// copy writes both via OSC 52, which works over SSH, and to the system
// clipboard, for terminals without OSC 52 such as Terminal.app. Either may
// be unavailable, so failures are ignored.
func (m *Model) copy(text string) tea.Cmd {
	system := func() tea.Msg {
		clipboard.WriteAll(text)
		return nil
	}
	return tea.Batch(tea.SetClipboard(text), system, m.notify("Copied "+strconv.Itoa(len([]rune(text)))+" characters"))
}

func (m *Model) notify(text string) tea.Cmd {
	m.toast = text
	m.toastID++
	id := m.toastID
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return toastEndMsg{id} })
}

func (m *Model) tick() tea.Cmd {
	if m.ticking || !(m.run.active || m.run.compacting() || m.shell != nil) {
		return nil
	}
	m.ticking = true
	return tea.Tick(time.Second/30, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) openAgent(name string) tea.Cmd {
	history, events, stop := m.conv.Agents().Subscribe(name)
	t := transcript.New()
	for _, ev := range history {
		t.Apply(ev)
	}
	hub := m.conv.Agents()
	m.closePage()
	m.page = &page{
		title:  name,
		view:   newChatView(t.Cells, emptyPage),
		live:   func() bool { return hub.IsActive(name) },
		t:      t,
		events: events,
		stop:   stop,
	}
	return waitAgent(m.page)
}

func waitAgent(p *page) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-p.events
		return agentPageMsg{p, ev, ok}
	}
}

func (m *Model) openCall(t *transcript.Tool) {
	heads := map[*transcript.Agent]transcript.Cell{}
	cells := func() []transcript.Cell {
		var out []transcript.Cell
		for _, a := range t.Agents() {
			if heads[a] == nil {
				heads[a] = &transcript.Notice{Level: transcript.Summary, Text: "▸ " + a.ID}
			}
			out = append(out, heads[a])
			out = append(out, a.Transcript.Cells()...)
		}
		return out
	}
	m.closePage()
	m.page = &page{
		title: "Sub-agents",
		view:  newChatView(cells, emptyPage),
		live:  func() bool { return t.State == transcript.Running },
	}
}

func (m *Model) closePage() {
	if m.page != nil && m.page.stop != nil {
		m.page.stop()
	}
	m.page = nil
}
