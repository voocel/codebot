package commands

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/ui/tui"
)

// Memory constructs the /memory command. With no args it prints the memory
// directory status; with `edit` it opens MEMORY.md in $EDITOR and reloads
// the session afterwards via the supplied callback.
func Memory(cwd string, reloadSession func()) Command {
	return NewSimple(Spec{
		Name: "memory", Usage: "/memory", Description: "Show or edit auto memory",
		Category: "info", Kind: KindBuiltin,
	}, func(inv Invocation) tea.Cmd {
		memDir := config.MemoryDir(cwd)
		memPath := config.MemoryFilePath(cwd)

		if len(inv.Args) > 0 && inv.Args[0] == "edit" {
			// Ensure file exists so editors that reject missing paths still work.
			config.EnsureMemoryDir(cwd)
			if _, err := os.Stat(memPath); os.IsNotExist(err) {
				_ = os.WriteFile(memPath, []byte("# Project Memory\n"), 0o644)
			}
			return OpenEditor(memPath, "Memory reloaded.", reloadSession)
		}

		var sb strings.Builder
		sb.WriteString("Auto Memory\n\n")
		fmt.Fprintf(&sb, "Directory:  %s\n", memDir)
		fmt.Fprintf(&sb, "Index:      %s\n", memPath)

		entries, _ := os.ReadDir(memDir)
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				files = append(files, e.Name())
			}
		}
		if len(files) == 0 {
			sb.WriteString("\nNo memory files yet. The LLM will create MEMORY.md as it learns.\n")
		} else {
			fmt.Fprintf(&sb, "\nFiles (%d):\n", len(files))
			for _, f := range files {
				sb.WriteString("  " + f + "\n")
			}
		}
		sb.WriteString("\nUse /memory edit to open MEMORY.md in your editor.")
		return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
	})
}

// OpenEditor launches $EDITOR (or vi as fallback) on path and runs onReload
// after the editor exits. Shared by /memory and /plan open.
func OpenEditor(path, successText string, onReload func()) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("editor: " + err.Error())}
		}
		if onReload != nil {
			onReload()
		}
		return tui.CommandResultMsg{Text: tui.SystemMsgStyle.Render(successText)}
	})
}
