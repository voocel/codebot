package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/ui/tui"
)

// TasksCommand implements InteractiveCommand for /tasks.
// Shows background bash commands and agent tasks in a grouped overlay.
type TasksCommand struct {
	app   *App
	state *tasksState
}

type tasksView int

const (
	tasksViewList tasksView = iota
	tasksViewDetail
)

// taskEntry is a unified item in the task list (either bash or agent).
type taskEntry struct {
	kind   string // "bash" | "agent"
	id     string
	desc   string
	status string
	// bash-specific
	shell *agentcoretools.BackgroundShell
	// agent-specific
	agent *agentcore.BackgroundTask
}

type tasksState struct {
	entries []taskEntry
	cursor  int
	view    tasksView
}

func NewTasksCommand(app *App) *TasksCommand {
	return &TasksCommand{app: app}
}

func (c *TasksCommand) Spec() CommandSpec {
	return CommandSpec{
		Name:        "tasks",
		Usage:       "/tasks",
		Description: "View and manage background tasks",
		Category:    "info",
		Kind:        CommandKindBuiltin,
	}
}

func (c *TasksCommand) Run(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
	entries := c.collectEntries()
	if len(entries) == 0 {
		return tui.SendCommandResult(tui.MutedStyle.Render("No background tasks."))
	}

	c.state = &tasksState{entries: entries}
	ctx.App.registry.SetOverlay(c)
	return nil
}

func (c *TasksCommand) Active() bool { return c.state != nil }

func (c *TasksCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch c.state.view {
	case tasksViewList:
		return c.handleListKey(msg)
	case tasksViewDetail:
		return c.handleDetailKey(msg)
	}
	return true, nil
}

func (c *TasksCommand) View(width int) string {
	if c.state == nil {
		return ""
	}
	switch c.state.view {
	case tasksViewList:
		return c.viewList(width)
	case tasksViewDetail:
		return c.viewDetail(width)
	}
	return ""
}

func (c *TasksCommand) Dismiss() { c.state = nil }

// --- Data collection ---

func (c *TasksCommand) collectEntries() []taskEntry {
	var entries []taskEntry

	if c.app.BashTool != nil {
		shells := c.app.BashTool.BackgroundShells()
		for i := range shells {
			s := &shells[i]
			desc := s.Description
			if desc == "" {
				desc = truncateStr(s.Command, 50)
			}
			entries = append(entries, taskEntry{
				kind:   "bash",
				id:     s.ID,
				desc:   desc,
				status: s.Status,
				shell:  s,
			})
		}
	}

	if c.app.SubAgentTool != nil {
		tasks := c.app.SubAgentTool.BackgroundTasks()
		for i := range tasks {
			t := &tasks[i]
			desc := t.Description
			if desc == "" {
				desc = truncateStr(t.Prompt, 50)
			}
			entries = append(entries, taskEntry{
				kind:   "agent",
				id:     t.ID,
				desc:   desc,
				status: t.Status,
				agent:  t,
			})
		}
	}

	return entries
}

func (c *TasksCommand) refresh() {
	c.state.entries = c.collectEntries()
	if c.state.cursor >= len(c.state.entries) {
		c.state.cursor = max(0, len(c.state.entries)-1)
	}
}

// refreshEntry updates a single entry in-place with latest data.
func (c *TasksCommand) refreshEntry(idx int) {
	if idx >= len(c.state.entries) {
		return
	}
	e := &c.state.entries[idx]
	switch e.kind {
	case "bash":
		if c.app.BashTool == nil {
			return
		}
		shells := c.app.BashTool.BackgroundShells()
		for i := range shells {
			if shells[i].ID == e.id {
				e.status = shells[i].Status
				e.shell = &shells[i]
				return
			}
		}
	case "agent":
		if c.app.SubAgentTool == nil {
			return
		}
		tasks := c.app.SubAgentTool.BackgroundTasks()
		for i := range tasks {
			if tasks[i].ID == e.id {
				e.status = tasks[i].Status
				e.agent = &tasks[i]
				return
			}
		}
	}
}

// --- List key handling ---

func (c *TasksCommand) handleListKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	s := c.state

	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		return true, nil

	case "down", "j":
		if s.cursor < len(s.entries)-1 {
			s.cursor++
		}
		return true, nil

	case "enter":
		if len(s.entries) > 0 {
			c.refresh() // get latest data before entering detail
			s.view = tasksViewDetail
			// Start live refresh if the selected task is running.
			if s.cursor < len(s.entries) && s.entries[s.cursor].status == "running" {
				return true, tui.TasksTickCmd()
			}
		}
		return true, nil

	case "x":
		if s.cursor < len(s.entries) {
			c.stopEntry(s.entries[s.cursor])
			c.refresh()
		}
		return true, nil

	case "r":
		c.refresh()
		return true, nil

	case "ctrl+f":
		if c.app.BashTool != nil {
			c.app.BashTool.StopAllBackgroundShells()
		}
		if c.app.SubAgentTool != nil {
			c.app.SubAgentTool.StopAllBackgroundTasks()
		}
		c.refresh()
		return true, nil

	case "esc", "ctrl+c":
		c.app.registry.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *TasksCommand) handleDetailKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	s := c.state

	switch msg.String() {
	case "esc":
		c.refresh()
		s.view = tasksViewList
		return true, nil

	case "x":
		if s.cursor < len(s.entries) {
			c.stopEntry(s.entries[s.cursor])
			c.refresh()
		}
		return true, nil
	}
	return true, nil
}

func (c *TasksCommand) stopEntry(e taskEntry) {
	switch e.kind {
	case "bash":
		if c.app.BashTool != nil {
			c.app.BashTool.StopBackgroundShell(e.id)
		}
	case "agent":
		if c.app.SubAgentTool != nil {
			c.app.SubAgentTool.StopBackgroundTask(e.id)
		}
	}
}

// --- List View ---

func (c *TasksCommand) viewList(_ int) string {
	s := c.state

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	activeStyle := lipgloss.NewStyle().Foreground(tui.ColorAccent).Bold(true)
	inactiveStyle := tui.MutedStyle
	groupStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("249"))

	// Separate by kind.
	var bashEntries, agentEntries []int
	var bashRunning, agentRunning int
	for i := range s.entries {
		switch s.entries[i].kind {
		case "bash":
			bashEntries = append(bashEntries, i)
			if s.entries[i].status == "running" {
				bashRunning++
			}
		case "agent":
			agentEntries = append(agentEntries, i)
			if s.entries[i].status == "running" {
				agentRunning++
			}
		}
	}

	totalRunning := bashRunning + agentRunning

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("  Background tasks"))
	sb.WriteString("\n")

	if totalRunning > 0 {
		var parts []string
		if bashRunning > 0 {
			parts = append(parts, fmt.Sprintf("%d active shell(s)", bashRunning))
		}
		if agentRunning > 0 {
			parts = append(parts, fmt.Sprintf("%d active agent(s)", agentRunning))
		}
		sb.WriteString(tui.MutedStyle.Render("  " + strings.Join(parts, " · ")))
	} else {
		sb.WriteString(tui.MutedStyle.Render("  No tasks currently running"))
	}
	sb.WriteString("\n")

	// Bash group.
	if len(bashEntries) > 0 {
		sb.WriteString("\n")
		sb.WriteString(groupStyle.Render(fmt.Sprintf("    Bashes (%d)", len(bashEntries))))
		sb.WriteString("\n")
		for _, idx := range bashEntries {
			c.renderListEntry(&sb, idx, activeStyle, inactiveStyle)
		}
	}

	// Agent group.
	if len(agentEntries) > 0 {
		sb.WriteString("\n")
		sb.WriteString(groupStyle.Render(fmt.Sprintf("    Local agents (%d)", len(agentEntries))))
		sb.WriteString("\n")
		for _, idx := range agentEntries {
			c.renderListEntry(&sb, idx, activeStyle, inactiveStyle)
		}
	}

	sb.WriteString("\n")
	hints := "  ↑/↓ to select · Enter to view · x to stop · r to refresh · Esc to close"
	if totalRunning > 0 {
		hints += " · ctrl+f to stop all"
	}
	sb.WriteString(tui.MutedStyle.Italic(true).Render(hints))

	return sb.String()
}

func (c *TasksCommand) renderListEntry(sb *strings.Builder, idx int, activeStyle, inactiveStyle lipgloss.Style) {
	e := c.state.entries[idx]
	status := renderTaskStatus(e.status)
	line := fmt.Sprintf("    %s %s", e.desc, status)
	if idx == c.state.cursor {
		sb.WriteString(activeStyle.Render("  > " + line[4:]))
	} else {
		sb.WriteString(inactiveStyle.Render(line))
	}
	sb.WriteByte('\n')
}

// --- Detail View ---

func (c *TasksCommand) viewDetail(width int) string {
	s := c.state
	if s.cursor >= len(s.entries) {
		return ""
	}

	// Refresh entry data for live output during rendering.
	c.refreshEntry(s.cursor)
	e := s.entries[s.cursor]

	switch e.kind {
	case "bash":
		return c.viewBashDetail(e, width)
	case "agent":
		return c.viewAgentDetail(e, width)
	}
	return ""
}

func (c *TasksCommand) viewBashDetail(e taskEntry, width int) string {
	bs := e.shell
	if bs == nil {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("247"))

	var sb strings.Builder

	sb.WriteString(titleStyle.Render("  Shell details"))
	sb.WriteString("\n\n")

	sb.WriteString(labelStyle.Render("  Status:  "))
	sb.WriteString(valueStyle.Render(renderTaskStatus(bs.Status)))
	sb.WriteString("\n")

	sb.WriteString(labelStyle.Render("  Runtime: "))
	sb.WriteString(valueStyle.Render(formatDuration(bs.StartedAt, bs.EndedAt)))
	sb.WriteString("\n")

	sb.WriteString(labelStyle.Render("  Command: "))
	sb.WriteString(valueStyle.Render(bs.Command))
	sb.WriteString("\n")

	if bs.OutputFile != "" {
		output := readFileTail(bs.OutputFile, 64*1024)
		if output != "" {
			sb.WriteString("\n")
			sb.WriteString(labelStyle.Render("  Output:"))
			sb.WriteString("\n")

			outLines := strings.Split(strings.TrimRight(output, "\n"), "\n")
			maxShow := 10
			start := 0
			if len(outLines) > maxShow {
				start = len(outLines) - maxShow
			}
			var boxLines []string
			for _, line := range outLines[start:] {
				boxLines = append(boxLines, valueStyle.Render(line))
			}
			innerWidth := max(width-8, 40)
			box := tui.DrawBox(boxLines, innerWidth, maxShow)
			for _, line := range strings.Split(box, "\n") {
				sb.WriteString("  " + line)
				sb.WriteString("\n")
			}
			if start > 0 {
				sb.WriteString(tui.MutedStyle.Italic(true).Render(fmt.Sprintf("  Showing %d lines", len(outLines)-start)))
				sb.WriteString("\n")
			}
		}
	}

	if bs.Status != "running" && bs.ExitCode != 0 {
		sb.WriteString("\n")
		sb.WriteString(tui.ErrorStyle.Render(fmt.Sprintf("  Exit code: %d", bs.ExitCode)))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	hint := "  Esc to go back"
	if bs.Status == "running" {
		hint += " · x to stop"
	}
	sb.WriteString(tui.MutedStyle.Italic(true).Render(hint))

	return sb.String()
}

func (c *TasksCommand) viewAgentDetail(e taskEntry, width int) string {
	t := e.agent
	if t == nil {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("247"))

	var sb strings.Builder

	// Header: agent > description
	header := t.Agent
	if t.Description != "" {
		header += " > " + t.Description
	}
	sb.WriteString(titleStyle.Render("  " + header))
	sb.WriteString("\n")

	// Stats line.
	runtime := formatDuration(t.StartedAt, t.EndedAt)
	tokens := formatTokenCount(t.TokensIn + t.TokensOut)
	stats := fmt.Sprintf("  %s · %s tokens · %d tools", runtime, tokens, t.ToolCount)
	sb.WriteString(labelStyle.Render(stats))
	sb.WriteString("\n\n")

	// Progress (tool calls).
	if len(t.Progress) > 0 {
		sb.WriteString(labelStyle.Render("  Progress"))
		sb.WriteString("\n")
		maxShow := 8
		start := 0
		if len(t.Progress) > maxShow {
			start = len(t.Progress) - maxShow
		}
		for i := start; i < len(t.Progress); i++ {
			prefix := "    "
			if i == len(t.Progress)-1 {
				prefix = "  > "
			}
			sb.WriteString(tui.MutedStyle.Render(prefix + t.Progress[i]))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Prompt.
	sb.WriteString(labelStyle.Render("  Prompt"))
	sb.WriteString("\n")
	for _, line := range strings.Split(t.Prompt, "\n") {
		sb.WriteString(valueStyle.Render("  " + line))
		sb.WriteString("\n")
	}

	// Output.
	if t.OutputFile != "" {
		output := readLastAssistantFromJSONL(t.OutputFile)
		if output != "" {
			sb.WriteString("\n")
			sb.WriteString(labelStyle.Render("  Output:"))
			sb.WriteString("\n")

			outLines := strings.Split(strings.TrimRight(output, "\n"), "\n")
			maxShow := 10
			start := 0
			if len(outLines) > maxShow {
				start = len(outLines) - maxShow
			}
			var boxLines []string
			for _, line := range outLines[start:] {
				boxLines = append(boxLines, valueStyle.Render(line))
			}
			innerWidth := max(width-8, 40)
			box := tui.DrawBox(boxLines, innerWidth, maxShow)
			for _, line := range strings.Split(box, "\n") {
				sb.WriteString("  " + line)
				sb.WriteString("\n")
			}
			if start > 0 {
				sb.WriteString(tui.MutedStyle.Italic(true).Render(fmt.Sprintf("  ── %d lines hidden ──", start)))
				sb.WriteString("\n")
			}
		}
	}

	if t.Status == "failed" && t.Error != "" {
		sb.WriteString("\n")
		sb.WriteString(tui.ErrorStyle.Render("  Error: " + t.Error))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	hint := "  Esc to go back"
	if t.Status == "running" {
		hint += " · x to stop"
	}
	sb.WriteString(tui.MutedStyle.Italic(true).Render(hint))

	return sb.String()
}

// --- Helpers ---

func renderTaskStatus(status string) string {
	switch status {
	case "running":
		return lipgloss.NewStyle().Foreground(tui.ColorPrimary).Render("(running)")
	case "completed":
		return lipgloss.NewStyle().Foreground(tui.ColorSuccess).Render("(completed)")
	case "failed":
		return tui.ErrorStyle.Render("(failed)")
	default:
		return tui.MutedStyle.Render("(" + status + ")")
	}
}

func formatDuration(start, end time.Time) string {
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(start)
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

func formatTokenCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func truncateStr(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// readFileTail reads up to the last maxBytes of a file.
// Sufficient for displaying the tail in the UI without OOM risk on large outputs.
func readFileTail(path string, maxBytes int64) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size == 0 {
		return ""
	}

	if size > maxBytes {
		f.Seek(size-maxBytes, 0)
	}
	data := make([]byte, min(size, maxBytes))
	n, _ := f.Read(data)
	return string(data[:n])
}

// readLastAssistantFromJSONL reads a jsonl file (one agentcore.Message per line)
// and returns the TextContent of the last assistant message.
func readLastAssistantFromJSONL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		var msg agentcore.Message
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Role == agentcore.RoleAssistant {
			if t := msg.TextContent(); t != "" {
				last = t
			}
		}
	}
	return last
}
