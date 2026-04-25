package commands

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
	"github.com/voocel/codebot/internal/ui/tui"
)

// HelpCommand drives /help — a tabbed modal overlay listing general intro,
// built-in commands, custom (file/plugin) commands, and skills.
type HelpCommand struct {
	registry Registry

	state *helpState
}

type helpState struct {
	active int
	width  int // captured from View() so body renderers can wrap long lines
}

var helpTabs = []string{"general", "built-in", "custom", "skills"}

// Help constructs the /help command. The registry is consumed both for
// listing peer commands and for installing the modal overlay.
func Help(registry Registry) *HelpCommand {
	return &HelpCommand{registry: registry}
}

func (c *HelpCommand) Spec() Spec {
	return Spec{
		Name:        "help",
		Usage:       "/help",
		Description: "Show this help",
		Category:    "info",
		Kind:        KindBuiltin,
	}
}

func (c *HelpCommand) Run(_ Invocation) tea.Cmd {
	c.state = &helpState{active: 0}
	c.registry.SetOverlay(c)
	return nil
}

func (c *HelpCommand) Active() bool  { return c.state != nil }
func (c *HelpCommand) IsModal() bool { return true }
func (c *HelpCommand) Dismiss()      { c.state = nil }

func (c *HelpCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "tab", "right", "l":
		c.state.active = (c.state.active + 1) % len(helpTabs)
		return true, nil
	case "shift+tab", "left", "h":
		c.state.active = (c.state.active - 1 + len(helpTabs)) % len(helpTabs)
		return true, nil
	case "1", "2", "3", "4":
		idx := int(msg.Runes[0] - '1')
		if idx < len(helpTabs) {
			c.state.active = idx
		}
		return true, nil
	case "esc", "ctrl+c", "q":
		c.registry.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *HelpCommand) View(width, height int) string {
	if c.state == nil {
		return ""
	}
	c.state.width = width
	frame := tui.InfoOverlayFrame{
		Title: "Codebot",
		Tabs: []tui.InfoOverlayTab{
			{Name: helpTabs[0], Body: c.renderGeneral},
			{Name: helpTabs[1], Body: c.renderBuiltin},
			{Name: helpTabs[2], Body: c.renderCustom},
			{Name: helpTabs[3], Body: c.renderSkills},
		},
		Active: c.state.active,
		Hint:   "Tab / ←→ switch · 1-4 jump · Esc close",
		Width:  width,
		Height: height,
	}
	return frame.Render()
}

func (c *HelpCommand) renderGeneral() string {
	// labelStyle uses Info to keep the left "key" column visually aligned
	// with the usage column in renderCommandGroup — same blue-teal accent
	// across every tab gives the help overlay one cohesive palette.
	labelStyle := lipgloss.NewStyle().Foreground(tui.Info)
	valueStyle := lipgloss.NewStyle().Foreground(tui.Text)
	sectionStyle := lipgloss.NewStyle().Foreground(tui.BrandSoft).Bold(true)

	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(valueStyle.Render("Terminal-native AI coding agent. " +
		"Understands your codebase, makes edits with your permission, and runs commands."))
	sb.WriteString("\n\n")

	row := func(k, v string) {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-18s", k)))
		sb.WriteString(valueStyle.Render(v))
		sb.WriteString("\n")
	}

	sb.WriteString(sectionStyle.Render("  Basics"))
	sb.WriteString("\n")
	row("/", "browse slash commands")
	row("!<cmd>", "run a shell command inline")
	row("Ctrl+V", "paste image from clipboard")
	row("Drop file", "attach an image by dragging it in")
	sb.WriteString("\n")

	sb.WriteString(sectionStyle.Render("  Shortcuts"))
	sb.WriteString("\n")
	row("Enter", "send message")
	row("Ctrl+J / Alt+Enter", "newline in input")
	row("Esc", "abort running agent")
	row("Ctrl+C", "quit (press twice to confirm)")
	row("Shift+Tab", "cycle permission mode")
	row("Up / Down", "navigate input history")
	row("Tab", "accept completion / suggestion")

	return strings.TrimRight(sb.String(), "\n")
}

func (c *HelpCommand) renderBuiltin() string {
	return c.renderCommandGroup(KindBuiltin,
		"No built-in commands registered.")
}

func (c *HelpCommand) renderCustom() string {
	return c.renderCommandGroup(KindCustom,
		"No custom commands. Add Markdown files under .codebot/commands/ to register slash commands.")
}

func (c *HelpCommand) renderSkills() string {
	return c.renderCommandGroup(KindSkill,
		"No skills loaded. Drop skill bundles into .codebot/skills/ or install via /plugins install.")
}

func (c *HelpCommand) renderCommandGroup(kind Kind, emptyMsg string) string {
	var cmds []Command
	for _, cmd := range c.registry.All() {
		spec := c.registry.EffectiveSpec(cmd)
		if spec.Hidden || spec.Kind != kind {
			continue
		}
		cmds = append(cmds, cmd)
	}
	if len(cmds) == 0 {
		return tui.MutedStyle.Render("  " + emptyMsg)
	}

	sort.SliceStable(cmds, func(i, j int) bool {
		return cmds[i].Spec().Name < cmds[j].Spec().Name
	})

	usageStyle := lipgloss.NewStyle().Foreground(tui.Info)
	descStyle := lipgloss.NewStyle().Foreground(tui.Text)
	metaStyle := lipgloss.NewStyle().Foreground(tui.Muted)

	const (
		leftPad     = "  " // 2-space gutter
		usageColumn = 22   // padding target for the usage cell
		colGap      = " "  // separator between usage and desc
	)
	// descIndent aligns the wrapped desc/tags row under the desc column on
	// the previous line (gutter + usage column + gap).
	descIndent := strings.Repeat(" ", len(leftPad)+usageColumn+len(colGap))

	// availWidth is the printable width inside the overlay frame; falls back
	// to 80 when the host hasn't reported a width yet (early renders).
	availWidth := c.state.width - 4 // matches inner = max(width-4,20) in InfoOverlayFrame
	if availWidth <= 0 {
		availWidth = 80
	}

	var sb strings.Builder
	for _, cmd := range cmds {
		spec := c.registry.EffectiveSpec(cmd)

		// Strip argument hints — /help is for browsing, the full syntax
		// (e.g. "/plugins [list|show|...] ...") belongs in the command
		// palette tooltip where the user is actually about to type.
		usage := "/" + spec.Name
		desc := spec.Description
		if desc == "" {
			desc = "(no description)"
		}

		var tags []string
		if spec.NeedsIdle {
			tags = append(tags, "idle")
		}
		if len(spec.Aliases) > 0 {
			tags = append(tags, "aliases: "+strings.Join(spec.Aliases, ","))
		}
		if spec.Source != "" {
			tags = append(tags, spec.Source)
		}
		tagText := ""
		if len(tags) > 0 {
			tagText = " [" + strings.Join(tags, " · ") + "]"
		}

		// writeDescBlock word-wraps desc to descMaxWidth, prefixing the first
		// line with initialPrefix and continuation lines with descIndent.
		// Tags glue onto the last desc line when they fit; otherwise spill to
		// their own continuation line. All desc/tag wrapping happens via
		// explicit "\n" so InfoOverlayFrame's line budget stays accurate.
		writeDescBlock := func(initialPrefix string, descMaxWidth int) {
			if descMaxWidth < 20 {
				descMaxWidth = 20
			}
			lines := strings.Split(strings.TrimRight(
				wordwrap.String(desc, descMaxWidth), "\n"), "\n")
			pendingTag := tagText
			for i, line := range lines {
				if i == 0 {
					sb.WriteString(initialPrefix)
				} else {
					sb.WriteString(descIndent)
				}
				sb.WriteString(descStyle.Render(line))
				if i == len(lines)-1 && pendingTag != "" &&
					len(line)+len(pendingTag) <= descMaxWidth {
					sb.WriteString(metaStyle.Render(pendingTag))
					pendingTag = ""
				}
				sb.WriteString("\n")
			}
			if pendingTag != "" {
				sb.WriteString(descIndent)
				sb.WriteString(metaStyle.Render(strings.TrimLeft(pendingTag, " ")))
				sb.WriteString("\n")
			}
		}

		if len(usage) > usageColumn {
			// Long usage gets its own line; desc wraps below at descIndent.
			sb.WriteString(leftPad)
			sb.WriteString(usageStyle.Render(usage))
			sb.WriteString("\n")
			writeDescBlock(descIndent, availWidth-len(descIndent))
			continue
		}

		// Standard layout: usage in left column, desc wraps in right column.
		initial := leftPad + usageStyle.Render(fmt.Sprintf("%-*s", usageColumn, usage)) + colGap
		writeDescBlock(initial, availWidth-len(leftPad)-usageColumn-len(colGap))
	}

	return strings.TrimRight(sb.String(), "\n")
}
