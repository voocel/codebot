package markdown

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
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
		glamour.WithStyles(customStyle()),
		glamour.WithWordWrap(width),
	)
	r.final = renderer
}

// customStyle returns a NoTTY-based style with proper markdown rendering.
// NoTTY avoids the ANSI padding bloat of DarkStyleConfig while keeping clean output.
func customStyle() ansi.StyleConfig {
	style := styles.NoTTYStyleConfig
	bold := true
	italic := true
	codeColor := "#89DDFF"
	headerColor := "#4EC9B0"
	subHeaderColor := "#FFCB6B"

	// Inline code: cyan, no backtick markers.
	style.Code.StylePrimitive.Color = &codeColor
	style.Code.StylePrimitive.BlockPrefix = ""
	style.Code.StylePrimitive.BlockSuffix = ""

	// Code block syntax highlighting.
	style.CodeBlock.Theme = "monokai"

	// Bold/italic: terminal styling, remove ** / * markers.
	style.Strong.Bold = &bold
	style.Strong.BlockPrefix = ""
	style.Strong.BlockSuffix = ""
	style.Emph.Italic = &italic
	style.Emph.BlockPrefix = ""
	style.Emph.BlockSuffix = ""

	// Headers: bold + color, remove # prefix markers.
	style.H1.StylePrimitive.Bold = &bold
	style.H1.StylePrimitive.Color = &headerColor
	style.H1.StylePrimitive.Prefix = ""
	style.H2.StylePrimitive.Bold = &bold
	style.H2.StylePrimitive.Color = &headerColor
	style.H2.StylePrimitive.Prefix = ""
	style.H3.StylePrimitive.Bold = &bold
	style.H3.StylePrimitive.Color = &headerColor
	style.H3.StylePrimitive.Prefix = ""
	style.H4.StylePrimitive.Bold = &bold
	style.H4.StylePrimitive.Color = &subHeaderColor
	style.H4.StylePrimitive.Prefix = ""
	style.H5.StylePrimitive.Bold = &bold
	style.H5.StylePrimitive.Prefix = ""
	style.H6.StylePrimitive.Bold = &bold
	style.H6.StylePrimitive.Prefix = ""

	return style
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
