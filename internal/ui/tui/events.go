package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
)

// printBlock prints content to terminal scrollback with a leading blank line.
// Every top-level output block (assistant reply, tool result, error) should
// use this instead of raw tea.Println so blocks are visually separated by
// exactly one blank line. Trailing newlines are stripped so that spacing
// between blocks is always consistent regardless of content construction.
func printBlock(content string) tea.Cmd {
	return tea.Println("\n" + strings.TrimRight(content, "\n"))
}

// HandleAgentEvent processes agent events.
// Completed content is printed to terminal scrollback via tea.Println.
// In-progress content (streaming, tool output) is shown in the live View().
func (m *Model) HandleAgentEvent(ev agentcore.Event) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch ev.Type {
	case agentcore.EventAgentStart:
		m.Running = true
		m.RunStats = runStats{StartedAt: time.Now()}
		m.ShowSummary = false
		m.clearSuggestion()

	case agentcore.EventAgentEnd:
		m.Running = false
		m.RunStats.Duration = time.Since(m.RunStats.StartedAt)
		m.RunStats.DisplayInput = m.RunStats.Input
		m.RunStats.DisplayOutput = m.RunStats.Output
		m.ShowSummary = true
		m.QueuedMsgs = nil
		clear(m.PendingTools)
		clear(m.ToolHeaders)
		clear(m.ToolOutputBuf)
		clear(m.ToolDeltaBuf)
		clear(m.ToolThinkingBuf)
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

			// Single printBlock to guarantee display order in scrollback.
			var block strings.Builder
			if thinkingText != "" {
				indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinkingText, 2)), 2)
				block.WriteString(ThinkingBodyStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
				block.WriteString("\n\n")
			}
			if content != "" {
				indented := m.renderMarkdownBlock(content, 2)
				block.WriteString(AssistantIconStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
			}
			if block.Len() > 0 {
				cmds = append(cmds, printBlock(block.String()))
			}
		}

	case agentcore.EventToolExecStart:
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
		} else {
			m.PendingTools[ev.ToolID] = label
			m.ToolHeaders[ev.ToolID] = ToolIconStyle.Render("● ") + RenderToolHeader(ev.Tool, ev.Args)
		}

	case agentcore.EventToolExecUpdate:
		switch ev.UpdateKind {
		case agentcore.ToolExecUpdatePreview:
			rendered := RenderEditResult(ev.Result)
			if rendered != "" {
				// Flush buffered header with the first preview.
				if header, ok := m.ToolHeaders[ev.ToolID]; ok {
					delete(m.ToolHeaders, ev.ToolID)
					cmds = append(cmds, printBlock(header+"\n"+indentBlock(rendered, 2)))
				} else {
					cmds = append(cmds, printBlock(indentBlock(rendered, 2)))
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
		delete(m.PendingTools, ev.ToolID)
		delete(m.ToolOutputBuf, ev.ToolID)
		delete(m.ToolDeltaBuf, ev.ToolID)
		delete(m.ToolThinkingBuf, ev.ToolID)

		// Build header + result as a single block so they stay grouped.
		header := m.ToolHeaders[ev.ToolID]
		delete(m.ToolHeaders, ev.ToolID)

		var body string
		if ev.Tool == "subagent" && !ev.IsError {
			content := FormatSubagentOutput(ev.Result)
			body = indentBlock(m.renderSubagentCard(content), 2)
		} else if ev.Tool == "edit" && !ev.IsError {
			body = indentBlock(RenderEditResult(ev.Result), 2)
		} else if ev.Tool == "write" && !ev.IsError {
			body = indentBlock(RenderWriteResult(ev.Result), 2)
		} else if (ev.Tool == "read" || ev.Tool == "glob") && !ev.IsError {
			body = indentBlock(RenderReadResult(ev.Result), 2)
		} else if ev.Tool == "ls" && !ev.IsError {
			dirPath, lsBody := RenderLsResult(ev.Result)
			if dirPath != "" {
				header = ToolIconStyle.Render("● ") + ToolNameStyle.Render("Ls") + ToolArgsStyle.Render("("+shortenPath(dirPath)+")")
			}
			body = indentBlock(lsBody, 2)
		} else {
			text := FormatToolResult(ev.Result, ev.IsError)
			text = m.wrapTextForIndent(text, 4)
			if ev.IsError {
				body = indentBlock(FormatToolOutput(text, 5, ErrorStyle), 2)
			} else {
				body = indentBlock(FormatToolOutput(text, 5), 2)
			}
		}

		var block string
		if body != "" {
			block = header + "\n" + body
		} else {
			block = header
		}
		cmds = append(cmds, printBlock(block))

	case agentcore.EventError:
		// Context cancellation is a normal operation (user Esc, plan submission stop).
		if ev.Err != nil && errors.Is(ev.Err, context.Canceled) {
			break
		}
		errMsg := "unknown error"
		if ev.Err != nil {
			errMsg = ev.Err.Error()
		}
		wrapped := indentBlock(ErrorStyle.Render(m.wrapTextForIndent("error: "+errMsg, 2)), 2)
		cmds = append(cmds, printBlock(wrapped))
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
