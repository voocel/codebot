package tui

// View-level primitives: the message/input building blocks that View() composes.
// Heavier domain renderers live in render_welcome.go / render_status.go /
// render_palette.go / render_summary.go / render_tool*.go.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// View-level helpers
// ---------------------------------------------------------------------------

// RenderPromptOutput renders a user message with optional welcome banner for scrollback.
func (m *Model) RenderPromptOutput(text string) string {
	userLine := "\n" + m.renderUserMessage(text)
	if m.ShowWelcome {
		return m.renderWelcome() + "\n" + userLine
	}
	return userLine
}

func (m *Model) shellInputActive() bool {
	return strings.HasPrefix(strings.TrimSpace(m.Input.Value()), "!")
}

// overlayView returns the rendered overlay content and whether it replaces the input area.
func (m *Model) overlayView() (string, bool) {
	if m.config.Overlay == nil {
		return "", false
	}
	ov := m.config.Overlay(m)
	if ov == nil {
		return "", false
	}
	return ov.View(m.Width, m.Height), ov.ReplacesInput
}

// renderCompletions renders the completion menu.
func (m *Model) renderCompletions() string {
	if !m.compActive || len(m.compItems) == 0 {
		return ""
	}
	return m.renderCommandPalette()
}

// styledInputView renders the textarea with optional command highlighting.
// When cmdHighlight is set, the command text in the view is colorized.
func (m *Model) styledInputView() string {
	view := m.Input.View()
	if m.shellInputActive() {
		view = strings.Replace(view, "❯", ShellAccentStyle.Render("❯"), 1)
		view = strings.Replace(view, "!", ShellAccentStyle.Render("!"), 1)
		return view
	}
	if m.cmdHighlight == "" {
		return view
	}
	colored := lipgloss.NewStyle().Foreground(Brand).Render(m.cmdHighlight)
	return strings.Replace(view, m.cmdHighlight, colored, 1)
}

// ---------------------------------------------------------------------------
// Input panel
// ---------------------------------------------------------------------------

// renderInputPanel renders the textarea together with the image-chip row above it.
func (m *Model) renderInputPanel() string {
	width := m.Width
	if width <= 0 {
		width = 80
	}

	var sections []string
	if len(m.Images) > 0 {
		var tags []string
		for i := range m.Images {
			tag := fmt.Sprintf("Image #%d", i+1)
			if m.ImageCursor == i {
				tags = append(tags, ImageSelectedStyle.Render("["+tag+"]"))
			} else {
				tags = append(tags, ImageTagStyle.Render(tag))
			}
		}
		line := strings.Join(tags, " ")
		if m.ImageCursor >= 0 {
			line += " " + InputHintStyle.Render("Delete remove · Esc cancel")
		} else {
			line += " " + InputHintStyle.Render("↑ select")
		}
		sections = append(sections, line)
	}

	inputView := m.styledInputView()
	sections = append(sections, inputView)

	content := strings.Join(sections, "\n\n")
	panelStyle := InputPanelStyle
	if m.shellInputActive() {
		panelStyle = ShellInputPanelStyle
	}
	return panelStyle.Width(max(width-2, 20)).Render(content)
}

// ---------------------------------------------------------------------------
// User message echo (scrollback)
// ---------------------------------------------------------------------------

// renderUserMessage renders a sent user message with the textarea's background.
//
// IMPORTANT: lipgloss Render() appends ANSI reset (\033[0m) after each call.
// If you nest styled content inside an outer Background style, the inner reset
// kills the outer background — causing "text has color A, padding has color B".
// To avoid this, every styled segment (icon, text, padding) must carry its own
// Background. This way each segment is self-contained and resets between them
// are invisible (zero characters wide).
func (m *Model) renderUserMessage(text string) string {
	wrapped := m.wrapTextForIndent(text, 2)
	lines := strings.Split(wrapped, "\n")
	bgStyle := lipgloss.NewStyle().Background(SurfaceAccent)
	prefixStyle := lipgloss.NewStyle().Foreground(RoleUser).Background(SurfaceAccent).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(RoleUser).Background(SurfaceAccent).Bold(true)
	padStyle := lipgloss.NewStyle().Background(SurfaceAccent)
	var out []string
	for i, line := range lines {
		prefix := "  "
		if i == 0 {
			prefix = prefixStyle.Render("❯") + bgStyle.Render(" ")
		}
		rendered := prefix + textStyle.Render(line)
		if pad := m.Width - lipgloss.Width(rendered); pad > 0 {
			rendered += padStyle.Render(strings.Repeat(" ", pad))
		}
		out = append(out, rendered)
	}
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------------------
// Markdown helpers
// ---------------------------------------------------------------------------

// RenderMarkdown renders a lightweight terminal-friendly markdown subset.
func (m *Model) RenderMarkdown(content string) string {
	if m.Markdown == nil || content == "" {
		return content
	}
	return m.Markdown.RenderFinal(content)
}

// renderMarkdownBlock renders complete markdown and applies only outer
// indentation. Markdown output is already wrapped by the renderer.
func (m *Model) renderMarkdownBlock(content string, indent int) string {
	if content == "" {
		return ""
	}
	if m.Markdown == nil {
		return indentBlock(m.wrapTextForIndent(content, indent), indent)
	}
	return indentBlock(m.RenderMarkdown(content), indent)
}
