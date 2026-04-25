package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ContextCommand drives /context — a tabbed modal overlay reporting current
// context window usage, message composition, and runtime suggestions.
type ContextCommand struct {
	session  *agent.Session
	registry Registry

	state *contextState
}

type contextState struct {
	active int

	snapshot       *agentcore.ContextSnapshot
	snapshotOK     bool
	contextUsage   *agentcore.ContextUsage
	breakdown      agent.ContextBreakdown
	suggestions    []agent.ContextSuggestion
	metrics        agent.RuntimeMetricsSnapshot
	lastCompaction agent.CompactionSnapshot
	hasCompaction  bool
}

var contextTabs = []string{"usage", "composition", "suggestions"}

// Context constructs the /context command.
func Context(session *agent.Session, registry Registry) *ContextCommand {
	return &ContextCommand{session: session, registry: registry}
}

func (c *ContextCommand) Spec() Spec {
	return Spec{
		Name:        "context",
		Usage:       "/context",
		Description: "Show current context snapshot",
		Category:    "info",
		Kind:        KindBuiltin,
	}
}

func (c *ContextCommand) Run(_ Invocation) tea.Cmd {
	snapshot, ok := c.session.ContextSnapshot()
	lastCompaction, hasCompaction := c.session.LastCompaction()
	c.state = &contextState{
		active:         0,
		snapshot:       snapshot,
		snapshotOK:     ok,
		contextUsage:   c.session.ContextUsage(),
		breakdown:      c.session.ContextBreakdown(),
		suggestions:    c.session.ContextSuggestions(),
		metrics:        c.session.RuntimeMetrics(),
		lastCompaction: lastCompaction,
		hasCompaction:  hasCompaction,
	}
	c.registry.SetOverlay(c)
	return nil
}

func (c *ContextCommand) Active() bool  { return c.state != nil }
func (c *ContextCommand) IsModal() bool { return true }
func (c *ContextCommand) Dismiss()      { c.state = nil }

func (c *ContextCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "tab", "right", "l":
		c.state.active = (c.state.active + 1) % len(contextTabs)
		return true, nil
	case "shift+tab", "left", "h":
		c.state.active = (c.state.active - 1 + len(contextTabs)) % len(contextTabs)
		return true, nil
	case "1", "2", "3":
		idx := int(msg.Runes[0] - '1')
		if idx < len(contextTabs) {
			c.state.active = idx
		}
		return true, nil
	case "esc", "ctrl+c", "q":
		c.registry.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *ContextCommand) View(width, height int) string {
	if c.state == nil {
		return ""
	}
	frame := tui.InfoOverlayFrame{
		Title: "Context",
		Tabs: []tui.InfoOverlayTab{
			{Name: contextTabs[0], Body: c.renderUsage},
			{Name: contextTabs[1], Body: c.renderComposition},
			{Name: contextTabs[2], Body: c.renderSuggestions},
		},
		Active: c.state.active,
		Hint:   "Tab / ←→ switch · 1-3 jump · Esc close",
		Width:  width,
		Height: height,
	}
	return frame.Render()
}

func (c *ContextCommand) renderUsage() string {
	s := c.state
	p := tui.NewInfoPanel("")

	usage := s.contextUsage
	if s.snapshotOK && s.snapshot != nil && s.snapshot.Usage != nil {
		usage = s.snapshot.Usage
	}
	if usage != nil {
		p.Row("Used", fmt.Sprintf("%s (%.1f%%)", tui.FormatTokens(usage.Tokens), usage.Percent))
		p.Row("Window", tui.FormatTokens(usage.ContextWindow))
		p.Hint("Detail", fmt.Sprintf("usage=%s, trailing=%s",
			tui.FormatTokens(usage.UsageTokens), tui.FormatTokens(usage.TrailingTokens)))
	} else {
		p.Hint("Used", "(unavailable)")
	}

	if s.breakdown.Total > 0 {
		window := s.breakdown.ContextWindow
		fmtPct := func(tokens int) string {
			if window <= 0 {
				return tui.FormatTokens(tokens)
			}
			return fmt.Sprintf("%s (%.0f%%)", tui.FormatTokens(tokens), float64(tokens)/float64(window)*100)
		}
		p.Section("Breakdown")
		p.Row("User text", fmtPct(s.breakdown.UserText))
		p.Row("Assistant text", fmtPct(s.breakdown.AssistantText))
		p.Row("Tool calls", fmtPct(s.breakdown.ToolCalls))
		p.Row("Tool results", fmtPct(s.breakdown.ToolResults))
		if s.breakdown.Summaries > 0 {
			p.Row("Summaries", fmtPct(s.breakdown.Summaries))
		}
		if s.breakdown.Images > 0 {
			p.Row("Images", fmtPct(s.breakdown.Images))
		}

		if len(s.breakdown.TopTools) > 0 {
			p.Section("Top tools")
			for _, t := range s.breakdown.TopTools {
				p.Row(t.Name, fmt.Sprintf("%s  calls=%s results=%s",
					fmtPct(t.Total), tui.FormatTokens(t.CallTokens), tui.FormatTokens(t.ResultTokens)))
			}
		}
	}

	return p.Render()
}

func (c *ContextCommand) renderComposition() string {
	s := c.state
	p := tui.NewInfoPanel("")

	if s.snapshotOK && s.snapshot != nil {
		p.Row("Scope", FormatContextScope(s.snapshot.Scope))
		if s.snapshot.TranscriptMessages != s.snapshot.ActiveMessages {
			p.Row("Messages", fmt.Sprintf("%d active / %d transcript",
				s.snapshot.ActiveMessages, s.snapshot.TranscriptMessages))
		} else {
			p.Row("Messages", fmt.Sprintf("%d", s.snapshot.ActiveMessages))
		}
		p.Row("Summaries", fmt.Sprintf("%d", s.snapshot.SummaryMessages))
		p.Row("Cleared results", fmt.Sprintf("%d", s.snapshot.ClearedToolResults))
		p.Row("Trimmed blocks", fmt.Sprintf("%d", s.snapshot.TrimmedTextBlocks))

		p.Section("Last rewrite")
		strategy := PrettyCompactionStrategy(s.snapshot.LastStrategy)
		if strategy == "" {
			strategy = "(none)"
		}
		p.Hint("Strategy", strategy)
		p.Row("Changed", FormatBool(s.snapshot.LastChanged))
		p.Hint("Details", FormatContextRewriteDetails(s.snapshot))
	} else {
		p.Hint("Snapshot", "(unavailable)")
	}

	p.Section("Compaction")
	p.Row("Total", fmt.Sprintf("%d", s.metrics.CompactionTotal))
	p.Row("Changed", fmt.Sprintf("%d", s.metrics.CompactionChanged))
	p.Row("Saved", tui.FormatTokens(s.metrics.CompactionSaved))
	p.Hint("By kind", FormatCompactionCounts(s.metrics.CompactionByKind))
	p.Hint("Last", FormatLastCompaction(s.lastCompaction, s.hasCompaction))

	return p.Render()
}

func (c *ContextCommand) renderSuggestions() string {
	s := c.state
	if len(s.suggestions) == 0 {
		return tui.MutedStyle.Render("  No suggestions. Context looks healthy.")
	}

	p := tui.NewInfoPanel("")
	for _, sug := range s.suggestions {
		msg := sug.Message
		if sug.Savings > 0 {
			msg += fmt.Sprintf(" (~%s saveable)", tui.FormatTokens(sug.Savings))
		}
		if sug.Severity == "warning" {
			p.Warn("!", msg)
		} else {
			p.Hint("i", msg)
		}
	}
	return strings.TrimRight(p.Render(), "\n")
}
