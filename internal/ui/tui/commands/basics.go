package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui"
)

// This file groups the small, single-screenful builtin commands that fit a
// NewSimple wrapper. Commands large enough to warrant their own file (Memory,
// Plugins, and the interactive overlays) live separately.

// Compact constructs the /compact command, which summarizes the history to
// free up the context window. The TUI reports the outcome from the
// compaction events.
func Compact(a *app.App) Command {
	return NewSimple(Spec{
		Name: "compact", Usage: "/compact", Description: "Compact conversation context", NeedsIdle: true, Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		conv := a.Current()
		return func() tea.Msg {
			if err := conv.Compact(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
				return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("Compaction failed: " + err.Error())}
			}
			return nil
		}
	})
}

// Copy constructs the /copy command which writes the last assistant response
// to the system clipboard.
func Copy(a *app.App) Command {
	return NewSimple(Spec{
		Name: "copy", Usage: "/copy", Description: "Copy last response to clipboard", Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		text := lastAssistantText(a.Current().History())
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

// lastAssistantText is the text of the latest assistant reply.
func lastAssistantText(history []agentcore.Message) string {
	for _, m := range slices.Backward(history) {
		if m.Role == litellm.RoleAssistant {
			if text := strings.TrimSpace(m.Text()); text != "" {
				return text
			}
		}
	}
	return ""
}

// Exit constructs the /exit command. Aliased as /quit and /q.
func Exit() Command {
	return NewSimple(Spec{
		Name: "exit", Aliases: []string{"quit", "q"},
		Usage: "/exit", Description: "Quit", Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		return func() tea.Msg { return tui.CommandResultMsg{Quit: true} }
	})
}

// MCP constructs the /mcp command which lists configured MCP servers and their
// connection / tool counts.
func MCP(a *app.App) Command {
	return NewSimple(Spec{
		Name: "mcp", Usage: "/mcp", Description: "Show MCP server status", Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			servers := a.MCPStatus(ctx)
			if len(servers) == 0 {
				return tui.CommandResultMsg{Text: tui.CommandStyle.Render("No MCP servers configured.")}
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
						tui.ToolIconStyle.Render("●"), s.Name, s.ToolCount)
					totalTools += s.ToolCount
					connected++
				}
			}
			fmt.Fprintf(&sb, "\nTotal: %d connected, %d failed, %d tools", connected, failed, totalTools)
			return tui.CommandResultMsg{Text: tui.CommandStyle.Render(sb.String())}
		}
	})
}

// NewSession constructs the /new command, aliased /clear, which starts a
// fresh session; the current one stays on disk for /resume.
func NewSession(a *app.App) Command {
	return NewSimple(Spec{
		Name: "new", Aliases: []string{"clear"}, Usage: "/new", Description: "Start a new session", NeedsIdle: true, Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		// Off the TUI's goroutine: opening publishes to it.
		return func() tea.Msg {
			if _, err := a.Open(""); err != nil {
				return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("Failed to create session: " + err.Error())}
			}
			return nil
		}
	})
}

// Reload constructs the /reload command which reloads plugins, skills and
// MCP servers from disk.
func Reload(a *app.App, t *Table) Command {
	return NewSimple(Spec{
		Name: "reload", Usage: "/reload", Description: "Reload skills, prompts, and plugins", NeedsIdle: true, Kind: KindBuiltin,
	}, func(_ Invocation) tea.Cmd {
		return func() tea.Msg {
			report, err := a.ReloadPlugins(context.Background())
			if err != nil {
				return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("Reload failed: " + err.Error())}
			}
			return tui.ApplyMsg{Apply: func() tea.Cmd {
				t.Rebuild() // new skills become commands
				return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf(
					"Reloaded: %d skills, %d MCP tools (%d connected, %d failed).",
					report.Skills, report.MCP.Tools, report.MCP.Connected, len(report.MCP.Errors),
				)))
			}}
		}
	})
}
