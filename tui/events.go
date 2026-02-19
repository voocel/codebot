package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
)

// HandleAgentEvent processes agent events.
// Completed content is printed to terminal scrollback via tea.Println.
// In-progress content (streaming, tool output) is shown in the live View().
func (m Model) HandleAgentEvent(ev agentcore.Event) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch ev.Type {
	case agentcore.EventAgentStart:
		m.Running = true
		m.RunStats = runStats{}
		m.ShowSummary = false

	case agentcore.EventAgentEnd:
		m.Running = false
		m.ShowSummary = true

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
				m.RunStats.Input += msg.Usage.Input
				m.RunStats.Output += msg.Usage.Output
			}

			content := strings.TrimSpace(ev.Message.TextContent())
			thinkingText := strings.TrimSpace(ev.Message.ThinkingContent())

			// Single tea.Println to guarantee display order in scrollback.
			var block strings.Builder
			block.WriteByte('\n')
			if thinkingText != "" {
				indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinkingText, 2)), 2)
				block.WriteString(ThinkingBodyStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
				block.WriteString("\n\n")
			}
			rendered := m.RenderMarkdown(content)
			indented := indentBlock(m.wrapTextForIndent(rendered, 2), 2)
			block.WriteString(AssistantIconStyle.Render("● ") + strings.TrimPrefix(indented, "  "))
			cmds = append(cmds, tea.Println(block.String()))
		}

	case agentcore.EventToolExecStart:
		m.PendingTools[ev.ToolID] = ev.Tool
		m.ToolOutputBuf[ev.ToolID] = &strings.Builder{}
		m.RunStats.ToolCalls++

		label := ev.Tool
		if ev.ToolLabel != "" {
			label = ev.ToolLabel
		}
		header := "\n" + ToolIconStyle.Render("● ") + ToolNameStyle.Render(label)
		if argsStr := FormatToolArgs(ev.Args); argsStr != "" {
			header += "\n" + indentBlock(ToolArgsStyle.Render(m.wrapTextForIndent(argsStr, 2)), 2)
		}
		cmds = append(cmds, tea.Println(header))

	case agentcore.EventToolExecUpdate:
		if line := FormatProgressLine(ev.Result); line != "" {
			if buf, ok := m.ToolOutputBuf[ev.ToolID]; ok {
				buf.WriteString(line)
				buf.WriteByte('\n')
			}
		}

	case agentcore.EventToolExecEnd:
		delete(m.PendingTools, ev.ToolID)
		delete(m.ToolOutputBuf, ev.ToolID)

		var rendered string
		if ev.Tool == "edit" && !ev.IsError {
			rendered = RenderEditResult(ev.Result)
		} else {
			rendered = FormatToolResult(ev.Result, ev.IsError)
		}
		style := ToolResultStyle
		if ev.IsError {
			style = ErrorStyle
		}
		cmds = append(cmds, tea.Println(indentBlock(style.Render(m.wrapTextForIndent(rendered, 2)), 2)))

	case agentcore.EventError:
		errMsg := "unknown error"
		if ev.Err != nil {
			errMsg = ev.Err.Error()
		}
		cmds = append(cmds, tea.Println("\n"+ErrorStyle.Render("  error: "+errMsg)))
	}

	if m.config.OnEvent != nil {
		if cmd := m.config.OnEvent(&m, ev); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}
