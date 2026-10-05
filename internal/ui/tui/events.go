package tui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/app"
)

// formatScrollbackBlock applies the project's standard spacing rules to a
// block of scrollback output: trailing newlines stripped, and optionally a
// leading blank line so the block is visually separated from what came
// before. Kept pure so the two print helpers and the tests can share it.
func formatScrollbackBlock(content string, inline bool) string {
	content = strings.TrimRight(content, "\n")
	if inline {
		return content
	}
	return "\n" + content
}

// scrollbackCacheLimit caps the replay cache. Beyond this we drop the
// oldest entries (FIFO). Only matters after a resize: the terminal's own
// scrollback is authoritative until handleResize wipes it with `\x1b[3J`
// and replays the cache. Sized generously so casual sessions never
// truncate — 5000 blocks at a few KB each is a few-MB ceiling.
const scrollbackCacheLimit = 5000

// Emit is the single entry point for writing content to terminal scrollback.
// It caches the exact body given to tea.Println so handleResize can replay
// the entire stream after a clear.
func (m *Model) Emit(body string) tea.Cmd {
	m.Scrollback = append(m.Scrollback, body)
	if overflow := len(m.Scrollback) - scrollbackCacheLimit; overflow > 0 {
		m.Scrollback = append(m.Scrollback[:0:0], m.Scrollback[overflow:]...)
	}
	return tea.Println(body)
}

// printBlock prints content to terminal scrollback with a leading blank
// line. Every top-level output block (assistant reply, tool result, error)
// should use this so blocks are visually separated by exactly one blank
// line.
func (m *Model) printBlock(content string) tea.Cmd {
	return m.Emit(formatScrollbackBlock(content, false))
}

// printInline prints content flush against the previous block (no leading
// blank line). Use for output that should feel like a direct continuation
// of what came before — e.g. shell command output under its echoed prompt.
func (m *Model) printInline(content string) tea.Cmd {
	return m.Emit(formatScrollbackBlock(content, true))
}

// startRun resets the live state for a run the conversation started.
func (m *Model) startRun() {
	m.Running = true
	m.clearStatusLine()
	m.RunStats = runStats{StartedAt: time.Now()}
	m.clearSuggestion()
}

// HandleAgentEvent processes agent events.
// Completed content is printed to terminal scrollback via tea.Println.
// In-progress content (streaming, tool output) is shown in the live View().
func (m *Model) HandleAgentEvent(ev agentcore.Event) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch e := ev.(type) {
	case agentcore.RunEnd:
		m.Running = false
		m.clearStatusLine()
		if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
			cmds = append(cmds, m.printBlock(indentBlock(ErrorStyle.Render(m.wrapTextForIndent(app.ErrorText(e.Err), 2)), 2)))
		}
		m.RunStats.Turns = e.Turns
		m.RunStats.Duration = time.Since(m.RunStats.StartedAt)
		m.RunStats.DisplayInput = m.RunStats.Input
		m.RunStats.DisplayOutput = m.RunStats.Output
		cmds = append(cmds, m.printBlock(m.renderRunSummary()))
		m.QueuedMsgs = nil
		clear(m.PendingTools)
		clear(m.HiddenToolCalls)
		clear(m.ToolHeaders)
		clear(m.ToolOutputBuf)
		clear(m.SubagentUsage)
		// A run cancelled mid-response ends without the response's
		// MessageEnd; clear the stream so the live area does not keep
		// painting it.
		m.IsStream = false
		m.Streaming.Reset()
		m.Thinking.Reset()
		// Any dialog still open after the run died has nobody listening for
		// its answer — release the blocked gate goroutines and clear the
		// queue. abort() is idempotent, so racing with a targeted dismiss
		// message is harmless.
		m.Dialogs.abortAll()

	case agentcore.Retry:
		// The failed response is discarded.
		m.IsStream = false
		m.Streaming.Reset()
		m.Thinking.Reset()
		m.StatusPrefix = fmt.Sprintf("Request failed, retrying (attempt %d)", e.Attempt)
		m.StatusDeadline = time.Now().Add(e.Delay)
		cmds = append(cmds, statusCountdownTick())

	case agentcore.CompactionStart:
		m.StatusPrefix, m.StatusDeadline = "Compacting context", time.Time{}

	case agentcore.CompactionEnd:
		m.clearStatusLine()
		switch {
		case e.Err != nil && !errors.Is(e.Err, context.Canceled):
			cmds = append(cmds, m.printBlock(indentBlock(ErrorStyle.Render("Compaction failed: "+e.Err.Error()), 2)))
		case e.Compaction != nil:
			cmds = append(cmds, m.printBlock(indentBlock(SystemMsgStyle.Render(
				fmt.Sprintf("Context compacted: %d messages summarized.", e.Compaction.Replaced)), 2)))
		case e.Err == nil:
			cmds = append(cmds, m.printBlock(indentBlock(MutedStyle.Render("Nothing to compact yet."), 2)))
		}

	case agentcore.MessageStart:
		m.clearStatusLine() // a retry's countdown ends with the response
		m.IsStream = true
		m.Streaming.Reset()
		m.Thinking.Reset()

	case agentcore.MessageDelta:
		if !m.IsStream {
			break
		}
		switch d := e.Event.(type) {
		case litellm.TextDelta:
			m.Streaming.WriteString(d.Text)
		case litellm.ReasoningDelta:
			m.Thinking.WriteString(d.Text)
		}

	case agentcore.MessageEnd:
		if e.Message.Role != litellm.RoleAssistant {
			break
		}
		m.IsStream = false
		m.Streaming.Reset()
		m.Thinking.Reset()
		if u := e.Message.Usage; u != nil {
			m.RunStats.Input += u.InputTokens
			m.RunStats.Output += u.OutputTokens
		}
		if reply := m.renderAssistantMessage(e.Message); reply != "" {
			cmds = append(cmds, m.printBlock(reply))
		}

	case agentcore.ToolStart:
		call := e.Call
		// A hidden call (app.HiddenToolCall) gets no header, output or
		// count; only the transcript leaves it out.
		if app.HiddenToolCall(call.Name, call.Args) {
			m.HiddenToolCalls[call.ID] = struct{}{}
			break
		}
		label := call.Name
		switch {
		case call.Name == "subagent":
			label, _ = parseSubagentHeader(call.Args)
		case call.Tool != nil && call.Tool.Label != "":
			label = call.Tool.Label
		}
		m.PendingTools[call.ID] = label
		m.ToolOutputBuf[call.ID] = &strings.Builder{}
		m.RunStats.ToolCalls++
		// The header is printed with the result, so that the calls of a
		// parallel batch each stay together; a previewed diff shows it
		// before the call is approved.
		header := toolCallHeader(call.Name, call.Args)
		if call.Preview == "" {
			m.ToolHeaders[call.ID] = header
			break
		}
		cmds = append(cmds, m.printBlock(header+"\n"+indentBlock(RenderDiff(call.Preview, extractPathArg(call.Args), m.diffBodyWidth()), 2)))

	case agentcore.ToolUpdate:
		buf, ok := m.ToolOutputBuf[e.Call.ID]
		if !ok {
			break
		}
		switch p := e.Progress.(type) {
		case string: // a line of bash output
			buf.WriteString(p)
			buf.WriteByte('\n')
		case subagent.Progress:
			m.subagentProgress(e.Call.ID, buf, p.Event)
		}

	case agentcore.ToolEnd:
		call := e.Call
		if call.Name == todo.ToolName && !e.Result.IsError {
			if items, err := todo.Parse(call.Args); err == nil {
				cmds = append(cmds, m.setTodos(items))
			}
		}
		_, hidden := m.HiddenToolCalls[call.ID]
		header := m.ToolHeaders[call.ID]
		usage := m.SubagentUsage[call.ID]
		delete(m.HiddenToolCalls, call.ID)
		delete(m.PendingTools, call.ID)
		delete(m.ToolHeaders, call.ID)
		delete(m.ToolOutputBuf, call.ID)
		delete(m.SubagentUsage, call.ID)
		if hidden {
			break
		}
		result := e.Result.Text()
		if usage != nil && !e.Result.IsError {
			result += "\n\n" + usage.String()
		}
		if block := m.renderToolCall(header, call.Name, call.Args, result, e.Result.IsError); block != "" {
			cmds = append(cmds, m.printBlock(block))
		}
	}

	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// subagentUsage sums what the sub-agent runs of a call used.
type subagentUsage struct {
	turns, tools, input, output int
}

func (u *subagentUsage) String() string {
	return fmt.Sprintf("%d turns · %d tools · ↑%s ↓%s tokens", u.turns, u.tools, FormatTokens(u.input), FormatTokens(u.output))
}

// subagentProgress shows an event of a sub-agent run of call id as a line
// of its output, and adds up what the run used.
func (m *Model) subagentProgress(id string, buf *strings.Builder, ev agentcore.Event) {
	usage := m.SubagentUsage[id]
	if usage == nil {
		usage = &subagentUsage{}
		m.SubagentUsage[id] = usage
	}
	var lines []string
	switch e := ev.(type) {
	case agentcore.MessageEnd:
		if e.Message.Role != litellm.RoleAssistant {
			break
		}
		if u := e.Message.Usage; u != nil {
			usage.input += u.InputTokens
			usage.output += u.OutputTokens
		}
		if text := oneLine(e.Message.Reasoning()); text != "" {
			lines = append(lines, ThinkingBodyStyle.Render("thinking "+ansi.Truncate(text, 71, "…")))
		}
		if text := oneLine(e.Message.Text()); text != "" {
			lines = append(lines, ReplyLabelStyle.Render("reply ")+ansi.Truncate(text, 74, "…"))
		}
	case agentcore.ToolStart:
		line := ToolNameStyle.Render(e.Call.Name)
		if hint := toolArgHint(e.Call.Args); hint != "" {
			line += MutedStyle.Render(" " + hint)
		}
		lines = append(lines, line)
	case agentcore.ToolEnd:
		if e.Result.IsError {
			lines = append(lines, ToolNameStyle.Render(e.Call.Name)+MutedStyle.Render(" failed"))
		}
	case agentcore.Retry:
		lines = append(lines, MutedStyle.Render(fmt.Sprintf("retry, attempt %d", e.Attempt)))
	case agentcore.RunEnd:
		usage.turns += e.Turns
		usage.tools += e.ToolCalls
	}
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
}

// oneLine is text trimmed, on a single line.
func oneLine(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
}

// clearStatusLine clears the live status and its countdown.
func (m *Model) clearStatusLine() {
	m.StatusPrefix = ""
	m.StatusDeadline = time.Time{}
}

// renderAssistantMessage renders a reply: its thinking, then its text.
func (m *Model) renderAssistantMessage(msg agentcore.Message) string {
	var parts []string
	if thinking := strings.TrimSpace(msg.Reasoning()); thinking != "" {
		indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinking, 2)), 2)
		parts = append(parts, ThinkingBodyStyle.Render("● ")+strings.TrimPrefix(indented, "  "))
	}
	if text := strings.TrimSpace(msg.Text()); text != "" {
		parts = append(parts, AssistantIconStyle.Render("● ")+strings.TrimPrefix(m.RenderMarkdownBlock(text, 2), "  "))
	}
	return strings.Join(parts, "\n\n")
}

// toolCallHeader renders the header line of a call of tool with args.
func toolCallHeader(tool string, args json.RawMessage) string {
	if tool != "subagent" {
		return ToolIconStyle.Render("● ") + RenderToolHeader(tool, args)
	}
	name, hint := parseSubagentHeader(args)
	header := ToolIconStyle.Render("● ") + ToolNameStyle.Render(name)
	if hint != "" {
		header += MutedStyle.Render(" → ") + ToolArgsStyle.Render(ansi.Truncate(hint, 80, "…"))
	}
	return header
}

// renderToolCall renders a finished tool call, live or restored: its header,
// with a red bullet if it failed, over the text of its result. An empty
// header means a preview already showed the header and the diff.
func (m *Model) renderToolCall(header, tool string, args json.RawMessage, result string, isError bool) string {
	if isError {
		if header == "" {
			header = toolCallHeader(tool, args)
		}
		// The bullet carries the failure; the muted body is detail.
		header = ErrorIconStyle.Render("● ") + strings.TrimPrefix(header, ToolIconStyle.Render("● "))
		text := m.wrapTextForIndent(FormatToolResult(result, true), 4)
		return joinBlock(header, indentBlock(FormatToolOutput(text, ToolResultMaxLines, MutedStyle), 2))
	}

	var body string
	switch tool {
	case "subagent":
		body = m.renderSubagentCard(cmp.Or(strings.TrimSpace(result), "(no output)"))
	case "edit":
		if header == "" {
			return ""
		}
		// The result is a line naming the file over the diff.
		_, diff, _ := strings.Cut(result, "\n")
		body = RenderDiff(diff, extractPathArg(args), m.diffBodyWidth())
	case "write":
		body = RenderWriteResult(result)
	case "read":
		body = RenderReadSummary(result)
	case "glob":
		body = RenderGlobResult(result)
	case "ls":
		var dir string
		dir, body = RenderLsResult(result)
		if dir != "" {
			header = ToolIconStyle.Render("● ") + ToolNameStyle.Render("Ls") + ToolArgsStyle.Render("("+ShortenPath(dir)+")")
		}
	default:
		text := m.wrapTextForIndent(FormatToolResult(result, false), 4)
		body = FormatToolOutput(text, ToolResultMaxLines)
	}
	return joinBlock(header, indentBlock(body, 2))
}

// joinBlock puts the non-empty of header and body one over the other.
func joinBlock(header, body string) string {
	if header == "" || body == "" {
		return header + body
	}
	return header + "\n" + body
}
