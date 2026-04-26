package commands

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
	"github.com/voocel/codebot/internal/ui/tui"
)

// TasksCommand drives /tasks — an interactive overlay listing background
// shells and forked sub-agents, with detail and stop controls.
type TasksCommand struct {
	runtime *agentcore.TaskRuntime
	overlay OverlayController

	state *tasksState
}

type tasksView int

const (
	tasksViewList tasksView = iota
	tasksViewDetail
)

type tasksState struct {
	entries []agentcore.BackgroundTaskEntry
	cursor  int
	view    tasksView
}

// Tasks constructs the /tasks command.
func Tasks(runtime *agentcore.TaskRuntime, overlay OverlayController) *TasksCommand {
	return &TasksCommand{runtime: runtime, overlay: overlay}
}

func (c *TasksCommand) Spec() Spec {
	return Spec{
		Name:        "tasks",
		Usage:       "/tasks",
		Description: "View and manage background tasks",
		Category:    "info",
		Kind:        KindBuiltin,
	}
}

func (c *TasksCommand) Run(_ Invocation) tea.Cmd {
	entries := c.listTasks()
	if len(entries) == 0 {
		return tui.SendCommandResult(tui.MutedStyle.Render("No background tasks."))
	}

	c.state = &tasksState{entries: entries}
	c.overlay.SetOverlay(c)
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

func (c *TasksCommand) View(width, _ int) string {
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

func (c *TasksCommand) listTasks() []agentcore.BackgroundTaskEntry {
	if c.runtime == nil {
		return nil
	}
	return c.runtime.List()
}

func (c *TasksCommand) refresh() {
	prevID := ""
	if c.state.cursor < len(c.state.entries) {
		prevID = c.state.entries[c.state.cursor].ID
	}
	c.state.entries = c.listTasks()
	if prevID != "" {
		for i, e := range c.state.entries {
			if e.ID == prevID {
				c.state.cursor = i
				return
			}
		}
	}
	if c.state.cursor >= len(c.state.entries) {
		c.state.cursor = max(0, len(c.state.entries)-1)
	}
}

func (c *TasksCommand) refreshEntry(idx int) {
	if idx >= len(c.state.entries) || c.runtime == nil {
		return
	}
	if latest := c.runtime.Get(c.state.entries[idx].ID); latest != nil {
		c.state.entries[idx] = *latest
	}
}

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
			c.refresh()
			s.view = tasksViewDetail
			if s.cursor < len(s.entries) && s.entries[s.cursor].Status == agentcore.TaskRunning {
				return true, tui.TasksTickCmd()
			}
		}
		return true, nil

	case "x":
		if s.cursor < len(s.entries) {
			c.stopTask(s.entries[s.cursor].ID)
			c.refresh()
		}
		return true, nil

	case "r":
		c.refresh()
		return true, nil

	case "ctrl+f":
		if c.runtime != nil {
			c.runtime.StopAll()
		}
		c.refresh()
		return true, nil

	case "esc", "ctrl+c":
		c.overlay.ClearOverlay()
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
			c.stopTask(s.entries[s.cursor].ID)
			c.refresh()
		}
		return true, nil
	}
	return true, nil
}

func (c *TasksCommand) stopTask(id string) {
	if c.runtime != nil {
		c.runtime.Stop(id)
	}
}

func (c *TasksCommand) viewList(_ int) string {
	s := c.state

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.Text)
	activeStyle := lipgloss.NewStyle().Foreground(tui.Accent).Bold(true)
	inactiveStyle := tui.MutedStyle
	groupStyle := lipgloss.NewStyle().Foreground(tui.Muted)

	var shellEntries, agentEntries []int
	var shellRunning, agentRunning int
	for i := range s.entries {
		switch s.entries[i].Type {
		case agentcore.TaskTypeShell:
			shellEntries = append(shellEntries, i)
			if s.entries[i].Status == agentcore.TaskRunning {
				shellRunning++
			}
		case agentcore.TaskTypeSubAgent:
			agentEntries = append(agentEntries, i)
			if s.entries[i].Status == agentcore.TaskRunning {
				agentRunning++
			}
		}
	}

	totalRunning := shellRunning + agentRunning

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("  Background tasks"))
	sb.WriteString("\n")

	if totalRunning > 0 {
		var parts []string
		if shellRunning > 0 {
			parts = append(parts, fmt.Sprintf("%d active shell(s)", shellRunning))
		}
		if agentRunning > 0 {
			parts = append(parts, fmt.Sprintf("%d active agent(s)", agentRunning))
		}
		sb.WriteString(tui.MutedStyle.Render("  " + strings.Join(parts, " · ")))
	} else {
		sb.WriteString(tui.MutedStyle.Render("  No tasks currently running"))
	}
	sb.WriteString("\n")

	if len(shellEntries) > 0 {
		sb.WriteString("\n")
		sb.WriteString(groupStyle.Render(fmt.Sprintf("    Shells (%d)", len(shellEntries))))
		sb.WriteString("\n")
		for _, idx := range shellEntries {
			c.renderListEntry(&sb, idx, activeStyle, inactiveStyle)
		}
	}

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
	desc := e.Description
	if desc == "" && e.Command != "" {
		desc = truncateStr(e.Command, 50)
	}
	status := renderTaskStatus(e.Status)
	line := fmt.Sprintf("    %s %s", desc, status)
	if idx == c.state.cursor {
		sb.WriteString(activeStyle.Render("  > " + line[4:]))
	} else {
		sb.WriteString(inactiveStyle.Render(line))
	}
	sb.WriteByte('\n')
}

func (c *TasksCommand) viewDetail(width int) string {
	s := c.state
	if s.cursor >= len(s.entries) {
		return ""
	}

	c.refreshEntry(s.cursor)
	e := s.entries[s.cursor]

	switch e.Type {
	case agentcore.TaskTypeShell:
		return c.viewShellDetail(e, width)
	case agentcore.TaskTypeSubAgent:
		return c.viewAgentDetail(e, width)
	}
	return ""
}

func (c *TasksCommand) viewShellDetail(e agentcore.BackgroundTaskEntry, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.Text)
	labelStyle := lipgloss.NewStyle().Foreground(tui.Muted)
	valueStyle := lipgloss.NewStyle().Foreground(tui.Text)

	var sb strings.Builder

	sb.WriteString(titleStyle.Render("  Shell details"))
	sb.WriteString("\n\n")

	sb.WriteString(labelStyle.Render("  Status:  "))
	sb.WriteString(valueStyle.Render(renderTaskStatus(e.Status)))
	sb.WriteString("\n")

	sb.WriteString(labelStyle.Render("  Runtime: "))
	sb.WriteString(valueStyle.Render(formatTaskDuration(e.StartedAt, e.EndedAt)))
	sb.WriteString("\n")

	sb.WriteString(labelStyle.Render("  Command: "))
	sb.WriteString(valueStyle.Render(e.Command))
	sb.WriteString("\n")

	if e.OutputFile != "" {
		output := readFileTail(e.OutputFile, 64*1024)
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

	if e.Status != agentcore.TaskRunning && e.ExitCode != 0 {
		sb.WriteString("\n")
		sb.WriteString(tui.ErrorStyle.Render(fmt.Sprintf("  Exit code: %d", e.ExitCode)))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	hint := "  Esc to go back"
	if e.Status == agentcore.TaskRunning {
		hint += " · x to stop"
	}
	sb.WriteString(tui.MutedStyle.Italic(true).Render(hint))

	return sb.String()
}

func (c *TasksCommand) viewAgentDetail(e agentcore.BackgroundTaskEntry, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.Text)
	labelStyle := lipgloss.NewStyle().Foreground(tui.Muted)
	valueStyle := lipgloss.NewStyle().Foreground(tui.Text)

	var sb strings.Builder

	header := e.Agent
	if e.Description != "" {
		header += " > " + e.Description
	}
	sb.WriteString(titleStyle.Render("  " + header))
	sb.WriteString("\n")

	runtime := formatTaskDuration(e.StartedAt, e.EndedAt)
	tokens := formatTaskTokens(e.TokensIn + e.TokensOut)
	stats := fmt.Sprintf("  %s · %s tokens · %d tools", runtime, tokens, e.ToolCount)
	sb.WriteString(labelStyle.Render(stats))
	sb.WriteString("\n\n")

	if e.Prompt != "" {
		sb.WriteString(labelStyle.Render("  Prompt"))
		sb.WriteString("\n")
		for _, line := range strings.Split(e.Prompt, "\n") {
			sb.WriteString(valueStyle.Render("  " + line))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if e.OutputFile != "" {
		output := readLastAssistantFromJSONL(e.OutputFile)
		if output != "" {
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

	if e.Status == agentcore.TaskFailed && e.Error != "" {
		sb.WriteString("\n")
		sb.WriteString(tui.ErrorStyle.Render("  Error: " + e.Error))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	hint := "  Esc to go back"
	if e.Status == agentcore.TaskRunning {
		hint += " · x to stop"
	}
	sb.WriteString(tui.MutedStyle.Italic(true).Render(hint))

	return sb.String()
}

func renderTaskStatus(status agentcore.TaskStatus) string {
	switch status {
	case agentcore.TaskRunning:
		return lipgloss.NewStyle().Foreground(tui.Brand).Render("(running)")
	case agentcore.TaskCompleted:
		return lipgloss.NewStyle().Foreground(tui.Success).Render("(completed)")
	case agentcore.TaskFailed:
		return tui.ErrorStyle.Render("(failed)")
	default:
		return tui.MutedStyle.Render("(" + string(status) + ")")
	}
}

func formatTaskDuration(start, end time.Time) string {
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(start)
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

func formatTaskTokens(n int) string {
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

// readFileTail reads up to maxBytes from the end of the file, used to keep
// large background-task output from blowing up the overlay render.
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

// readLastAssistantFromJSONL parses a jsonl transcript (one agentcore.Message
// per line) and returns the text of the last assistant message.
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
