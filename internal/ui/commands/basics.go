package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/ui/tui"
)

// This file groups the small, single-screenful builtin commands that fit a
// NewSimple wrapper. Commands large enough to warrant their own file (Plan,
// Memory, Loop, Plugins, and the interactive overlays) live separately.

// Clear constructs the /clear command which wipes the in-memory conversation
// (session history on disk is preserved). resetPlanState is invoked so any
// pending plan-mode UI state is dropped at the same time.
func Clear(session *agent.Session, resetPlanState func()) Command {
	return NewSimple(Spec{
		Name: "clear", Usage: "/clear", Description: "Clear current context (memory only)",
		Category: "session", NeedsIdle: true, Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		session.ClearConversation()
		resetPlanState()
		return func() tea.Msg {
			return tui.CommandResultMsg{
				Text:  tui.SystemMsgStyle.Render("Current context cleared (session history is kept)."),
				Clear: true,
			}
		}
	})
}

// Compact constructs the /compact command which collapses old conversation
// history into a summary to free up the context window.
func Compact(session *agent.Session) Command {
	return NewSimple(Spec{
		Name: "compact", Usage: "/compact", Description: "Compact conversation context",
		Category: "session", NeedsIdle: true, Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		run := func() tea.Msg {
			result, err := session.Compact()
			if err != nil {
				return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("Compaction failed: " + err.Error())}
			}
			if !result.Changed {
				return tui.CommandResultMsg{Text: tui.MutedStyle.Render("Context unchanged; nothing worth compacting yet.")}
			}
			return tui.CommandResultMsg{
				Text: tui.SystemMsgStyle.Render(fmt.Sprintf(
					"Context compacted: %s -> %s.",
					tui.FormatTokens(result.TokensBefore),
					tui.FormatTokens(result.TokensAfter),
				)),
			}
		}
		return tea.Sequence(
			tui.SendCommandResult(tui.MutedStyle.Render("Compacting context...")),
			run,
		)
	})
}

// Copy constructs the /copy command which writes the last assistant response
// to the system clipboard.
func Copy(session *agent.Session) Command {
	return NewSimple(Spec{
		Name: "copy", Usage: "/copy", Description: "Copy last response to clipboard",
		Category: "info", Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		text := session.LastAssistantText()
		if text == "" {
			return tui.SendCommandResult(tui.ErrorStyle.Render("No assistant response to copy."))
		}
		if err := clipboard.WriteAll(text); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Clipboard write failed: " + err.Error()))
		}
		n := len([]rune(text))
		return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf("Copied %d characters to clipboard.", n)))
	})
}

// Exit constructs the /exit command. Aliased as /quit and /q.
func Exit() Command {
	return NewSimple(Spec{
		Name: "exit", Aliases: []string{"quit", "q"},
		Usage: "/exit", Description: "Quit",
		Category: "exit", Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		return func() tea.Msg { return tui.CommandResultMsg{Quit: true} }
	})
}

// MCP constructs the /mcp command which lists configured MCP servers and their
// connection / tool counts.
func MCP(manager *mcpclient.Manager) Command {
	return NewSimple(Spec{
		Name: "mcp", Usage: "/mcp", Description: "Show MCP server status",
		Category: "info", Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		if manager == nil {
			return tui.SendCommandResult(tui.CommandStyle.Render("No MCP servers configured."))
		}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			servers := manager.Status(ctx)
			if len(servers) == 0 {
				return tui.CommandResultMsg{Text: tui.CommandStyle.Render("No MCP servers found.")}
			}

			var connected, failed int
			totalTools := 0
			var sb strings.Builder
			sb.WriteString("\nMCP Servers:\n")
			for _, s := range servers {
				switch {
				case s.Error != "":
					fmt.Fprintf(&sb, "%s %-20s %s\n",
						tui.ErrorStyle.Render("●"), s.Name, tui.ErrorStyle.Render(s.Error))
					failed++
				case s.ListError != "":
					fmt.Fprintf(&sb, "%s %-20s %s\n",
						tui.ErrorStyle.Render("●"), s.Name, tui.ErrorStyle.Render("tools/list: "+s.ListError))
					connected++
				default:
					fmt.Fprintf(&sb, "%s %-20s %d tools\n",
						tui.DiffAddStyle.Render("●"), s.Name, s.ToolCount)
					totalTools += s.ToolCount
					connected++
				}
			}
			fmt.Fprintf(&sb, "\nTotal: %d connected, %d failed, %d tools", connected, failed, totalTools)
			return tui.CommandResultMsg{Text: tui.CommandStyle.Render(sb.String())}
		}
	})
}

// New constructs the /new command which abandons the current session and
// starts a fresh one. Plan-mode UI state is reset alongside.
func New(session *agent.Session, resetPlanState func()) Command {
	return NewSimple(Spec{
		Name: "new", Usage: "/new", Description: "Start new session",
		Category: "session", NeedsIdle: true, Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		if err := session.Reset(); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to create session: " + err.Error()))
		}
		resetPlanState()
		return func() tea.Msg {
			return tui.CommandResultMsg{
				Text:  tui.SystemMsgStyle.Render("New session started."),
				Clear: true,
			}
		}
	})
}

// ReloadResult summarises the outcome of a /reload run, used to render the
// terminal feedback line. The host App fills in the counts after reloading
// plugin / skill / MCP state.
type ReloadResult struct {
	Commands     int
	Skills       int
	MCPTools     int
	MCPConnected int
	MCPFailed    int
}

// Reload constructs the /reload command which rebuilds the plugin/skill/MCP
// runtime from disk. The host provides the reload callback returning the
// outcome counts (or an error to render).
func Reload(reload func() (ReloadResult, error)) Command {
	return NewSimple(Spec{
		Name: "reload", Usage: "/reload", Description: "Reload skills, prompts, and commands",
		Category: "session", NeedsIdle: true, Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		result, err := reload()
		if err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Reload failed: " + err.Error()))
		}
		return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf(
			"Reloaded: %d commands, %d skills, %d MCP tools (%d connected, %d failed).",
			result.Commands, result.Skills, result.MCPTools, result.MCPConnected, result.MCPFailed,
		)))
	})
}

// Session constructs the /session command which prints the current session's
// id, paths, and accumulated cost.
func Session(session *agent.Session) Command {
	return NewSimple(Spec{
		Name: "session", Usage: "/session", Description: "Show current session info",
		Category: "info", Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		info, err := session.CurrentSessionInfo()
		if err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to load session info: " + err.Error()))
		}
		name := info.ID
		if info.Name != "" {
			name = info.Name + " (" + info.ID + ")"
		}
		text := fmt.Sprintf("Session: %s\nPath: %s\nCwd: %s\nCreated: %s",
			name, info.Path, info.Cwd, info.Created.Format("2006-01-02 15:04:05"))

		inTok, outTok, cost := session.CostEstimate()
		if inTok+outTok > 0 {
			text += fmt.Sprintf("\nCost: ~$%.4f (%s in / %s out)",
				cost, tui.FormatTokens(inTok), tui.FormatTokens(outTok))
		}

		return tui.SendCommandResult(tui.CommandStyle.Render(text))
	})
}
