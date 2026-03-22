package markdown

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// Renderer renders markdown for the TUI using glamour.
type Renderer struct {
	width int
	final *glamour.TermRenderer
}

// NewRenderer creates a markdown renderer at the given width.
func NewRenderer(width int) *Renderer {
	r := &Renderer{}
	r.SetWidth(width)
	return r
}

// SetWidth rebuilds the underlying renderer when terminal width changes.
func (r *Renderer) SetWidth(width int) {
	if width <= 0 {
		width = 80
	}
	if r.width == width && r.final != nil {
		return
	}
	r.width = width
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStyles(styles.DarkStyleConfig),
		glamour.WithWordWrap(width),
	)
	r.final = renderer
}

// RenderFinal renders final markdown to ANSI-formatted terminal output.
func (r *Renderer) RenderFinal(content string) string {
	if content == "" {
		return ""
	}
	if r == nil || r.final == nil {
		return strings.TrimSpace(content)
	}
	rendered, err := r.final.Render(content)
	if err != nil {
		return strings.TrimSpace(content)
	}
	return strings.TrimSpace(dedent(rendered))
}

// RenderLiveString currently uses the same renderer as final output.
func (r *Renderer) RenderLiveString(content string) string {
	return r.RenderFinal(content)
}

// Width reports the current renderer width.
func (r *Renderer) Width() int {
	if r == nil {
		return 0
	}
	return r.width
}

func dedent(s string) string {
	lines := strings.Split(s, "\n")
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return s
	}
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}
	return strings.Join(lines, "\n")
}
