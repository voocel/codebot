package tui

import (
	"os/exec"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/imageinput"
)

// handleCommandKey takes the keys that act on the input as a whole:
// Shift+Tab cycles the permission mode, and Enter runs a "!shell" line or a
// "/command" line instead of sending it.
func (m *Model) handleCommandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "shift+tab":
		if m.Running {
			return m, nil, false
		}
		i := slices.Index(interact.Modes, m.Mode)
		m.app.SetMode(interact.Modes[(i+1)%len(interact.Modes)])
		return m, nil, true
	case "enter":
	default:
		return m, nil, false
	}

	text := strings.TrimSpace(m.Input.Value())
	if shell, ok := strings.CutPrefix(text, "!"); ok {
		shell = strings.TrimSpace(shell)
		if shell == "" {
			return m, nil, false
		}
		m.Input.Reset()
		m.ShowWelcome = false
		return m, tea.Sequence(m.Emit(m.RenderPromptOutput(text)), runShell(shell, m.conv.Cwd())), true
	}

	if !strings.HasPrefix(text, "/") {
		return m, nil, false
	}
	// Commands are "/word ..."; a path like "/Users/..." has a second slash
	// before any space.
	name := text[1:]
	if i := strings.IndexAny(name, " \t"); i > 0 {
		name = name[:i]
	}
	if strings.Contains(name, "/") {
		return m, nil, false
	}
	m.Input.Reset()
	m.ShowWelcome = false
	return m, tea.Sequence(m.Emit(m.RenderPromptOutput(text)), m.commands.Run(text)), true
}

// overlay is the interactive command on screen, or nil.
func (m *Model) overlay() *OverlayState {
	return m.commands.Overlay()
}

// runShell runs a "!" line in dir and shows its output.
func runShell(cmd, dir string) tea.Cmd {
	return func() tea.Msg {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = dir
		out, err := c.CombinedOutput()
		result := strings.TrimRight(string(out), "\n")
		if err != nil && result != "" {
			result += "\n" + err.Error()
		} else if err != nil {
			result = err.Error()
		}
		return CommandResultMsg{Text: CommandStyle.Render(result), Inline: true}
	}
}

// pasteClipboard attaches the clipboard's image, or pastes its text when it
// holds none.
func pasteClipboard() tea.Msg {
	data, err := imageinput.ReadImage()
	if err != nil {
		return PasteErrorMsg{Text: ErrorStyle.Render("clipboard: " + err.Error())}
	}
	if data == nil {
		return PasteTextMsg{}
	}
	block, err := imageinput.FromBytes(data)
	if err != nil {
		return PasteErrorMsg{Text: ErrorStyle.Render(err.Error())}
	}
	return ImageAttachedMsg{Block: block}
}

// dropImage attaches the image a drag-and-drop pasted the path of; nil when
// text is not an image path.
func dropImage(text string) tea.Cmd {
	path := imageinput.ParseDroppedPath(text)
	if path == "" {
		return nil
	}
	return func() tea.Msg {
		block, err := imageinput.LoadFile(path)
		if err != nil {
			return PasteErrorMsg{Text: ErrorStyle.Render(err.Error())}
		}
		return ImageAttachedMsg{Block: block}
	}
}
