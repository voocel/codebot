package tui

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
)

// isPlanFileTool reports whether a write/edit tool call targets a file under
// the plans directory. Plan-file write/edit calls render as a status-only
// line ("Plan") so the tool log isn't drowned out by the model's incremental
// edits. The full plan body surfaces once exit_plan_mode succeeds.
//
// `cwd` lets us resolve relative paths the same way agentcore's write/edit
// tool does (ResolvePath(WorkDir, path)), so a relative-path write still
// matches when it lands inside plansDir.
func isPlanFileTool(tool, cwd, plansDir string, args json.RawMessage) bool {
	if plansDir == "" {
		return false
	}
	if tool != "write" && tool != "edit" {
		return false
	}
	if len(args) == 0 {
		return false
	}
	var parsed struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return false
	}
	target := parsed.FilePath
	if target == "" {
		target = parsed.Path
	}
	if target == "" {
		return false
	}
	if !filepath.IsAbs(target) && cwd != "" {
		target = filepath.Join(cwd, target)
	}
	cleaned := filepath.Clean(target)
	root := filepath.Clean(plansDir)
	rel, err := filepath.Rel(root, cleaned)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

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
// the entire stream after a clear. All pre-formatted paths — printBlock,
// printInline, and direct tea.Println calls that used to exist — funnel
// through here so the cache stays authoritative. Exported so the outer
// ui package (app.go, plan.go) can push external scrollback writes
// through the same cache.
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

// FlushStreamingAssistant prints the current live assistant stream into
// scrollback before a programmatic abort can skip EventMessageEnd.
func (m *Model) FlushStreamingAssistant() tea.Cmd {
	if m.Streaming == nil || m.Thinking == nil {
		return nil
	}
	content := strings.TrimSpace(m.Streaming.String())
	thinkingText := strings.TrimSpace(m.Thinking.String())
	if content == "" && thinkingText == "" {
		return nil
	}

	var block strings.Builder
	if thinkingText != "" {
		indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinkingText, 2)), 2)
		block.WriteString(ThinkingBodyStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
		if content != "" {
			block.WriteString("\n\n")
		}
	}
	if content != "" {
		indented := m.RenderMarkdownBlock(content, 2)
		block.WriteString(AssistantIconStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
		m.SuppressNextAssistantText = content
	}

	m.IsStream = false
	m.Streaming.Reset()
	m.Thinking.Reset()
	return m.printBlock(block.String())
}

// HandleAgentEvent processes agent events.
// Completed content is printed to terminal scrollback via tea.Println.
// In-progress content (streaming, tool output) is shown in the live View().
func (m *Model) HandleAgentEvent(ev agentcore.Event) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch ev.Type {
	case agentcore.EventAgentStart:
		m.Running = true
		m.clearRetryStatus()
		m.RunStats = runStats{StartedAt: time.Now()}
		m.clearSuggestion()

	case agentcore.EventAgentEnd:
		m.Running = false
		m.clearRetryStatus()
		m.RunStats.Duration = time.Since(m.RunStats.StartedAt)
		m.RunStats.DisplayInput = m.RunStats.Input
		m.RunStats.DisplayOutput = m.RunStats.Output
		cmds = append(cmds, m.printBlock(m.renderRunSummary()))
		m.QueuedMsgs = nil
		clear(m.PendingTools)
		clear(m.HiddenToolCalls)
		clear(m.ToolHeaders)
		clear(m.ToolOutputBuf)
		clear(m.ToolDeltaBuf)
		clear(m.ToolThinkingBuf)
		// Defensive stream cleanup: normally EventMessageEnd clears these,
		// but a mid-message abort (e.g. exit_plan_mode → Session.AbortSilent())
		// fires AgentEnd without ever firing MessageEnd, leaving IsStream=true
		// and a populated Streaming buffer behind. View() then keeps painting
		// the assistant bullet + spinner forever, even after the plan card is
		// dismissed. Reset here so the live area returns to a clean state on
		// any agent termination — completion OR abort.
		m.IsStream = false
		m.Streaming.Reset()
		m.Thinking.Reset()
		m.SuppressNextAssistantText = ""
		if m.AskUser != nil {
			close(m.AskUser.respCh)
			m.AskUser = nil
		}

	case agentcore.EventTurnStart:
		m.TurnCount++
		m.RunStats.Turns++

	case agentcore.EventMessageStart:
		if ev.Message.GetRole() == agentcore.RoleAssistant {
			m.IsStream = true
			m.Streaming.Reset()
			m.Thinking.Reset()
		}

	case agentcore.EventMessageUpdate:
		if !m.IsStream {
			break
		}
		if ev.Message != nil {
			if text := ev.Message.TextContent(); text != "" {
				m.Streaming.Reset()
				m.Streaming.WriteString(text)
			}
			if thinking := ev.Message.ThinkingContent(); thinking != "" {
				m.Thinking.Reset()
				m.Thinking.WriteString(thinking)
			}
		} else if ev.Delta != "" {
			m.Streaming.WriteString(ev.Delta)
		}

	case agentcore.EventMessageEnd:
		if ev.Message.GetRole() == agentcore.RoleAssistant {
			m.IsStream = false
			m.Streaming.Reset()
			m.Thinking.Reset()

			// Accumulate token usage via type assertion (AgentMessage has no Usage method).
			if msg, ok := ev.Message.(agentcore.Message); ok && msg.Usage != nil {
				m.RunStats.Input += msg.Usage.Input + msg.Usage.CacheRead + msg.Usage.CacheWrite
				m.RunStats.Output += msg.Usage.Output
			}

			content := strings.TrimSpace(ev.Message.TextContent())
			thinkingText := strings.TrimSpace(ev.Message.ThinkingContent())
			if content != "" && content == m.SuppressNextAssistantText {
				content = ""
				thinkingText = ""
				m.SuppressNextAssistantText = ""
			}

			// Single printBlock to guarantee display order in scrollback.
			var block strings.Builder
			if thinkingText != "" {
				indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinkingText, 2)), 2)
				block.WriteString(ThinkingBodyStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
				block.WriteString("\n\n")
			}
			if content != "" {
				indented := m.RenderMarkdownBlock(content, 2)
				block.WriteString(AssistantIconStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
			}
			if block.Len() > 0 {
				cmds = append(cmds, m.printBlock(block.String()))
			}
		}

	case agentcore.EventToolExecStart:
		// Hidden tools (task_*) skip the visible pipeline entirely: no header,
		// no output buffer, no tool count. The call still happens in the agent
		// loop — only the TUI side is silent.
		if IsHiddenToolCall(ev.Tool, ev.Args) {
			m.HiddenToolCalls[ev.ToolID] = struct{}{}
			break
		}
		label := ev.Tool
		if ev.ToolLabel != "" {
			label = ev.ToolLabel
		}
		m.ToolOutputBuf[ev.ToolID] = &strings.Builder{}
		m.RunStats.ToolCalls++

		// Buffer the header — it will be printed together with the result
		// at EventToolExecEnd so parallel tools stay grouped.
		if ev.Tool == "subagent" {
			name, hint := parseSubagentHeader(ev.Args)
			m.PendingTools[ev.ToolID] = name
			header := ToolIconStyle.Render("● ") + ToolNameStyle.Render(name)
			if hint != "" {
				header += MutedStyle.Render(" → ") + ToolArgsStyle.Render(truncateRunes(hint, 80))
			}
			m.ToolHeaders[ev.ToolID] = header
		} else if isPlanFileTool(ev.Tool, m.Cwd, m.PlansDir, ev.Args) {
			// Plan files render as a single status line ("Plan"). Incremental
			// write/edit on the plan file would otherwise spam the tool log
			// with diffs of an artifact the user will see in full once
			// exit_plan_mode succeeds. The "Plan" label in PendingTools doubles
			// as the marker EventToolExecEnd reads to suppress the diff body —
			// agentcore drops Args from End events so per-call state must come
			// from somewhere set at Start.
			m.PendingTools[ev.ToolID] = "Plan"
			m.ToolHeaders[ev.ToolID] = ToolIconStyle.Render("● ") + ToolNameStyle.Render("Plan")
		} else {
			m.PendingTools[ev.ToolID] = label
			m.ToolHeaders[ev.ToolID] = ToolIconStyle.Render("● ") + RenderToolHeader(ev.Tool, ev.Args)
		}

	case agentcore.EventToolExecUpdate:
		if _, hidden := m.HiddenToolCalls[ev.ToolID]; hidden || IsHiddenToolCall(ev.Tool, ev.Args) {
			break
		}
		switch ev.UpdateKind {
		case agentcore.ToolExecUpdatePreview:
			// Plan files: the preview pipeline is skipped entirely. The header
			// alone (rendered at EventToolExecEnd as "● Plan" + footer hint)
			// avoids paint thrash from many edits.
			if m.PendingTools[ev.ToolID] == "Plan" {
				break
			}
			rendered := RenderEditResult(ev.Result, m.diffBodyWidth())
			if rendered != "" {
				// Flush buffered header with the first preview.
				if header, ok := m.ToolHeaders[ev.ToolID]; ok {
					delete(m.ToolHeaders, ev.ToolID)
					cmds = append(cmds, m.printBlock(header+"\n"+indentBlock(rendered, 2)))
				} else {
					cmds = append(cmds, m.printBlock(indentBlock(rendered, 2)))
				}
			}
		case agentcore.ToolExecUpdateProgress:
			if ev.Progress == nil {
				break
			}
			switch ev.Progress.Kind {
			case agentcore.ProgressToolDelta:
				if ev.Progress.Delta == "" {
					break
				}
				buf := m.ToolDeltaBuf[ev.ToolID]
				if buf == nil {
					buf = &strings.Builder{}
					m.ToolDeltaBuf[ev.ToolID] = buf
				}
				buf.WriteString(ev.Progress.Delta)
			case agentcore.ProgressThinking:
				if ev.Progress.Thinking == "" {
					break
				}
				buf := m.ToolThinkingBuf[ev.ToolID]
				if buf == nil {
					buf = &strings.Builder{}
					m.ToolThinkingBuf[ev.ToolID] = buf
				}
				buf.Reset()
				buf.WriteString(ev.Progress.Thinking)
			default:
				if line := FormatProgressLine(ev.Progress); line != "" {
					if buf, ok := m.ToolOutputBuf[ev.ToolID]; ok {
						// Flush accumulated thinking/delta before the new tool/turn line.
						m.flushSubagentStreaming(ev.ToolID, buf)
						buf.WriteString(line)
						buf.WriteByte('\n')
					}
				}
			}
		}

	case agentcore.EventToolExecEnd:
		if _, hidden := m.HiddenToolCalls[ev.ToolID]; hidden || IsHiddenToolCall(ev.Tool, ev.Args) {
			delete(m.HiddenToolCalls, ev.ToolID)
			delete(m.PendingTools, ev.ToolID)
			delete(m.ToolHeaders, ev.ToolID)
			delete(m.ToolOutputBuf, ev.ToolID)
			delete(m.ToolDeltaBuf, ev.ToolID)
			delete(m.ToolThinkingBuf, ev.ToolID)
			break
		}
		delete(m.HiddenToolCalls, ev.ToolID)
		// agentcore drops Args from End events, so we recover the plan-file
		// marker from PendingTools (set to "Plan" at Start) before deleting it.
		// Subagent's same-name collision is impossible: subagent End is dispatched
		// by the `ev.Tool == "subagent"` branch below, never reaching the plan-file
		// branch.
		isPlanFile := m.PendingTools[ev.ToolID] == "Plan"
		delete(m.PendingTools, ev.ToolID)
		delete(m.ToolOutputBuf, ev.ToolID)
		delete(m.ToolDeltaBuf, ev.ToolID)
		delete(m.ToolThinkingBuf, ev.ToolID)

		// Build header + result as a single block so they stay grouped.
		header := m.ToolHeaders[ev.ToolID]
		delete(m.ToolHeaders, ev.ToolID)
		if ev.IsError {
			// Retint the bullet red but keep the args summary captured at
			// EventToolExecStart — agentcore drops Args from the End event
			// (see loop.go), so re-rendering from ev.Args alone would lose
			// the command/path the user needs to read the error.
			okBullet := ToolIconStyle.Render("● ")
			redBullet := ErrorIconStyle.Render("● ")
			if rest, ok := strings.CutPrefix(header, okBullet); ok {
				header = redBullet + rest
			} else if header == "" {
				header = redBullet + RenderToolHeader(ev.Tool, ev.Args)
			} else {
				header = redBullet + header
			}
		}

		var body string
		if ev.Tool == "subagent" && !ev.IsError {
			content := FormatSubagentOutput(ev.Result)
			body = indentBlock(m.renderSubagentCard(content), 2)
		} else if isPlanFile && !ev.IsError {
			// Plan files: hide the 12-line diff. The full plan body surfaces
			// once exit_plan_mode succeeds (see plan.go renderPlanForReview);
			// during incremental edits we only show a one-line affordance.
			body = indentBlock(MutedStyle.Render("/plan to preview"), 2)
		} else if ev.Tool == "edit" && !ev.IsError {
			body = indentBlock(RenderEditResult(ev.Result, m.diffBodyWidth()), 2)
		} else if ev.Tool == "write" && !ev.IsError {
			body = indentBlock(RenderWriteResult(ev.Result), 2)
		} else if ev.Tool == "read" && !ev.IsError {
			body = indentBlock(RenderReadSummary(ev.Result), 2)
		} else if ev.Tool == "glob" && !ev.IsError {
			body = indentBlock(RenderReadResult(ev.Result), 2)
		} else if ev.Tool == "ls" && !ev.IsError {
			dirPath, lsBody := RenderLsResult(ev.Result)
			if dirPath != "" {
				header = ToolIconStyle.Render("● ") + ToolNameStyle.Render("Ls") + ToolArgsStyle.Render("("+ShortenPath(dirPath)+")")
			}
			body = indentBlock(lsBody, 2)
		} else {
			text := FormatToolResult(ev.Result, ev.IsError)
			text = m.wrapTextForIndent(text, 4)
			if ev.IsError {
				body = indentBlock(FormatToolOutput(text, ToolResultMaxLines, ErrorStyle), 2)
			} else {
				body = indentBlock(FormatToolOutput(text, ToolResultMaxLines), 2)
			}
		}

		var block string
		if body != "" {
			if header != "" {
				block = header + "\n" + body
			} else {
				block = body
			}
		} else {
			block = header
		}
		if block != "" {
			cmds = append(cmds, m.printBlock(block))
		}

	case agentcore.EventError:
		m.clearRetryStatus()
		// Context cancellation is a normal operation (user Esc, plan submission stop).
		if ev.Err != nil && errors.Is(ev.Err, context.Canceled) {
			break
		}
		errMsg := "unknown error"
		if ev.Err != nil {
			errMsg = ev.Err.Error()
		}
		wrapped := indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: "+errMsg, 2)), 2)
		cmds = append(cmds, m.printBlock(wrapped))
	}

	if m.config.OnEvent != nil {
		if cmd := m.config.OnEvent(m, ev); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// clearRetryStatus removes the retry countdown from the live area.
// Stops the next tick from doing anything (View() short-circuits on empty Prefix).
func (m *Model) clearRetryStatus() {
	m.RetryPrefix = ""
	m.RetryDeadline = time.Time{}
}

// flushSubagentStreaming writes accumulated thinking/delta to the output buffer as single lines.
func (m *Model) flushSubagentStreaming(toolID string, buf *strings.Builder) {
	if tbuf, ok := m.ToolThinkingBuf[toolID]; ok && tbuf.Len() > 0 {
		text := strings.ReplaceAll(strings.TrimSpace(tbuf.String()), "\n", " ")
		buf.WriteString(ThinkingBodyStyle.Render("thinking " + truncateRunes(text, 71)))
		buf.WriteByte('\n')
		tbuf.Reset()
	}
	if dbuf, ok := m.ToolDeltaBuf[toolID]; ok && dbuf.Len() > 0 {
		text := strings.ReplaceAll(strings.TrimSpace(dbuf.String()), "\n", " ")
		buf.WriteString(ReplyLabelStyle.Render("reply ") + truncateRunes(text, 74))
		buf.WriteByte('\n')
		dbuf.Reset()
	}
}
