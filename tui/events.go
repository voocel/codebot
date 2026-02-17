package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
)

// HandleAgentEvent processes standard agent events and updates the model.
// After standard handling, it delegates to the OnEvent hook if configured.
func (m Model) HandleAgentEvent(ev agentcore.Event) (Model, tea.Cmd) {
	switch ev.Type {
	case agentcore.EventAgentStart:
		m.Running = true

	case agentcore.EventAgentEnd:
		m.Running = false

	case agentcore.EventTurnStart:
		m.TurnCount++

	case agentcore.EventMessageStart:
		if role := MsgRole(ev.Message); role == agentcore.RoleAssistant {
			m.IsStream = true
			m.Streaming.Reset()
		} else if role == agentcore.RoleUser {
			if ev.Message != nil && ev.Message.TextContent() != "" {
				m.Blocks = append(m.Blocks, Block{
					Kind:    BlockUser,
					Content: UserPrefixStyle.Render("> You (steer)") + "\n" + ev.Message.TextContent(),
				})
			}
		}

	case agentcore.EventMessageUpdate:
		if m.IsStream {
			m.Streaming.WriteString(ev.Delta)
			m.RebuildViewport()
		}

	case agentcore.EventMessageEnd:
		if role := MsgRole(ev.Message); role == agentcore.RoleAssistant {
			m.IsStream = false
			content := m.Streaming.String()
			m.Streaming.Reset()

			rendered := m.RenderMarkdown(content)
			m.Blocks = append(m.Blocks, Block{
				Kind:    BlockAssistant,
				Content: AssistantPrefixStyle.Render("> Assistant") + "\n" + rendered,
			})
			m.RebuildViewport()
		}

	case agentcore.EventToolExecStart:
		m.PendingTools[ev.ToolID] = ev.Tool
		argsStr := FormatToolArgs(ev.Args)
		m.Blocks = append(m.Blocks, Block{
			Kind:      BlockToolStart,
			Content:   ToolNameStyle.Render("  > "+ev.Tool) + " " + MutedStyle.Render(argsStr),
			Collapsed: m.ToolsCollapsed,
			Summary:   ev.Tool + ": " + argsStr,
		})
		m.ToolOutputBuf[ev.ToolID] = &strings.Builder{}
		m.Blocks = append(m.Blocks, Block{Kind: BlockToolEnd, Collapsed: m.ToolsCollapsed})
		m.ToolOutputIdx[ev.ToolID] = len(m.Blocks) - 1
		m.RebuildViewport()

	case agentcore.EventToolExecUpdate:
		if line := FormatProgressLine(ev.Result); line != "" {
			if buf, ok := m.ToolOutputBuf[ev.ToolID]; ok {
				buf.WriteString(line)
				buf.WriteByte('\n')
				content := RenderStreamingOutput(buf.String(), 10)
				if idx, ok := m.ToolOutputIdx[ev.ToolID]; ok && idx < len(m.Blocks) {
					m.Blocks[idx] = Block{Kind: BlockToolEnd, Content: content}
				}
				m.RebuildViewport()
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
		if idx, ok := m.ToolOutputIdx[ev.ToolID]; ok && idx < len(m.Blocks) {
			m.Blocks[idx] = Block{Kind: BlockToolEnd, Content: rendered}
			delete(m.ToolOutputIdx, ev.ToolID)
		} else {
			m.Blocks = append(m.Blocks, Block{Kind: BlockToolEnd, Content: rendered})
		}
		m.RebuildViewport()

	case agentcore.EventError:
		errMsg := "unknown error"
		if ev.Err != nil {
			errMsg = ev.Err.Error()
		}
		m.Blocks = append(m.Blocks, Block{
			Kind:    BlockError,
			Content: ErrorStyle.Render("  Error: " + errMsg),
		})
		m.RebuildViewport()
	}

	// Delegate to hook for extended handling (e.g. thinking blocks)
	var cmd tea.Cmd
	if m.config.OnEvent != nil {
		cmd = m.config.OnEvent(&m, ev)
	}
	return m, cmd
}
