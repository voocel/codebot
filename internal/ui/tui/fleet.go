package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	cbteam "github.com/voocel/codebot/internal/team"
)

// Fleet list — a live roster of observable agents (long-lived teammates and
// background sub-agents) pinned just below the input. Keyboard focus normally
// sits in the textarea; pressing ↓ at the last input line drops focus into this
// list (handleDownKey → FleetFocus). While focused, ↑/↓ move the highlight and
// Enter confirms — an agent previews its live transcript (split layout, the
// list stays pinned below), the "main" row returns to the conversation. The
// same flow works whether the upper area is currently the conversation or a
// transcript, so the user can switch targets from the list at any time.
//
// Row 0 is always "main" — the leader conversation. Selecting it (Enter) just
// returns focus to the input, so the list is a symmetric switcher: drop in,
// pick an agent to inspect, or pick main to come back. Agents occupy rows 1..N,
// so cursor index i>=1 maps to agents[i-1].
//
// Data source is the teammate event hub: its KnownAgents() is exactly "which
// agents can I preview right now (or replay if ended)", so Enter maps 1:1 to
// openTranscriptModal with no name reconciliation. Teammates always appear;
// background sub-agents appear once SubagentHubObserver routes them in.

// maxFleetVisible caps how many agent rows render so a long-running session
// with many finished agents can't push the input off-screen. Active agents
// sort first, so the cap only ever hides already-ended entries.
const maxFleetVisible = 6

// fleetAgents returns the hub's known agents sorted active-first, then by name.
// nil when no hub is wired or nothing has published yet.
func (m *Model) fleetAgents() []cbteam.AgentInfo {
	if m.config.TeammateEvents == nil {
		return nil
	}
	infos := m.config.TeammateEvents.KnownAgents()
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].Active != infos[j].Active {
			return infos[i].Active // active before ended
		}
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// fleetEnterable reports whether ↓ should drop focus into the list: only when
// at least one agent is currently live. The inline list is a momentum view of
// running work — finished agents are reviewable via the Ctrl+O modal but don't
// keep the list pinned below the input at idle.
func (m *Model) fleetEnterable() bool {
	return m.config.TeammateEvents != nil && len(m.config.TeammateEvents.ActiveAgents()) > 0
}

// handleFleetKey intercepts navigation while focus is in the fleet list.
// Returns handled=false (without consuming) when the list isn't focused, so
// normal input handling proceeds.
//
// While focused, the list claims only the navigation/action keys below
// (arrows, Enter, Esc, PgUp/PgDn, x, Ctrl+F). Any *other* key — notably a
// printable character — hands control straight back to the input: it exits the
// list (re-focusing the textarea) and falls through so the keystroke lands in
// the box. So the cursor "drops" into the list but typing instantly jumps back
// up to the input. (No letter shortcuts like j/k, which would otherwise swallow
// the first character of whatever the user types.)
//
// Cursor 0 is the "main" row; cursors 1..len(agents) map to agents[cursor-1].
//
// One consistent model regardless of what's currently shown above (the
// conversation or a live transcript): ↑/↓ only move the highlight, and Enter
// confirms — an agent row previews/switches to that agent (the upper pane swaps
// in place, the list stays pinned below), the main row returns to the
// conversation. Navigation alone never tears the preview down, so the user can
// freely move the highlight anywhere and only commit with Enter.
func (m *Model) handleFleetKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !m.FleetFocus {
		return m, nil, false
	}
	agents := m.fleetAgents()
	if len(agents) == 0 {
		m.fleetExit()
		return m, nil, true
	}
	last := len(agents) // cursor range is [0, last]: 0=main, 1..last=agents
	m.clampFleetCursor(last)

	switch msg.String() {
	case "up":
		if m.FleetCursor <= 0 {
			m.fleetExit() // ↑ past the top row returns to the input
			return m, nil, true
		}
		m.FleetCursor-- // navigation only moves the highlight; Enter confirms
		return m, nil, true
	case "down":
		if m.FleetCursor < last {
			m.FleetCursor++
		}
		return m, nil, true
	case "enter":
		if m.FleetCursor == 0 {
			m.fleetExit() // select "main" → back to the conversation/input
			return m, nil, true
		}
		// Confirm: preview the highlighted agent. Open it if nothing is showing,
		// switch in place if another agent is, no-op if it's already current.
		name := agents[m.FleetCursor-1].Name
		if m.TranscriptModal == nil {
			return m, m.openTranscriptModal(name), true
		}
		return m, m.switchTranscriptAgent(name), true
	case "esc":
		m.fleetExit()
		return m, nil, true
	case "x":
		// Stop the selected agent; stay in the list so the user can stop
		// others. The task's abort produces EventAgentEnd → hub MarkStopped →
		// the row flips to "ended" on the next render. (No-op on the main row.)
		if m.FleetCursor >= 1 && m.config.StopAgent != nil {
			m.config.StopAgent(agents[m.FleetCursor-1].Name)
		}
		return m, nil, true
	case "ctrl+f":
		if m.config.StopAllAgents != nil {
			m.config.StopAllAgents()
		}
		return m, nil, true
	case "pgup":
		if m.TranscriptModal != nil {
			m.TranscriptModal.PageUp()
		}
		return m, nil, true
	case "pgdown":
		if m.TranscriptModal != nil {
			m.TranscriptModal.PageDown()
		}
		return m, nil, true
	}
	// Anything else (a printable character, ctrl+c, …) belongs to the input:
	// drop list focus — which re-focuses the textarea — and fall through so the
	// key is handled normally (typed into the box, or routed to the global
	// ctrl+c / etc.).
	m.fleetExit()
	return m, nil, false
}

// fleetExit drops fleet focus and tears down any inline preview, handing the
// keyboard back to the input (its cursor resumes).
func (m *Model) fleetExit() {
	m.FleetFocus = false
	m.closeTranscriptModal()
	m.Input.Focus()
}

// clampFleetCursor keeps the cursor within [0, agentCount] — 0 is the "main"
// row, 1..agentCount are agents. Shared by the key handler and the renderer so
// the bound stays in one place as the agent list grows and shrinks.
func (m *Model) clampFleetCursor(agentCount int) {
	if m.FleetCursor > agentCount {
		m.FleetCursor = agentCount
	}
	if m.FleetCursor < 0 {
		m.FleetCursor = 0
	}
}

// renderFleetList renders the agent roster below the input. Returns "" when no
// agent has been observed. The selection cursor only shows while focused; when
// unfocused the list is a passive momentum view of what's running.
//
// Layout: each agent row carries its live elapsed time right-aligned, while the
// "main" row — which needs no timer — carries the keybind hint at the far right
// instead.
func (m *Model) renderFleetList() string {
	agents := m.fleetAgents()
	if len(agents) == 0 {
		return ""
	}
	// Momentum view: render only while something is live, or while the user is
	// actively navigating the list (don't yank it out from under them).
	if !m.FleetFocus && !m.fleetEnterable() {
		return ""
	}
	m.clampFleetCursor(len(agents))

	width := m.Width
	if width <= 0 {
		width = 80
	}

	var sb strings.Builder

	// Row 0: main — the leader conversation. Its right edge holds the hint
	// (it has no elapsed time), keeping the list self-documenting.
	mainSel := m.FleetFocus && m.FleetCursor == 0
	mainLeft := rowPrefix(mainSel) + fleetDot(mainSel) + fleetName("main", mainSel)
	sb.WriteString(fleetRow(mainLeft, m.fleetHint(), width))
	sb.WriteByte('\n')

	visible := agents
	hidden := 0
	if len(visible) > maxFleetVisible {
		hidden = len(visible) - maxFleetVisible
		visible = visible[:maxFleetVisible]
	}

	for i, a := range visible {
		selected := m.FleetFocus && m.FleetCursor == i+1

		name := a.Name
		if !a.Active {
			name += " (ended)"
		}
		left := rowPrefix(selected) + fleetDot(selected) + fleetName(name, selected)

		// Live elapsed, right-aligned, for active agents that correlate to a
		// running task.
		right := ""
		if a.Active && m.config.FleetAgentStat != nil {
			if elapsed, ok := m.config.FleetAgentStat(a.Name); ok {
				right = MutedStyle.Render(formatDuration(elapsed))
			}
		}
		sb.WriteString(fleetRow(left, right, width))
		sb.WriteByte('\n')
	}
	if hidden > 0 {
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("  … +%d more", hidden)))
		sb.WriteByte('\n')
	}

	return strings.TrimRight(sb.String(), "\n")
}

// renderFleetSplit composes the split-preview screen: the confirmed agent's
// live transcript on top, a horizontal divider, then the fleet list pinned at
// the bottom (replacing the input). The upper pane always shows whichever agent
// the user last confirmed with Enter — moving the highlight elsewhere in the
// list doesn't change it until Enter, so the two halves stay coherent.
func (m *Model) renderFleetSplit() string {
	list := m.renderFleetList()

	width := m.Width
	if width <= 0 {
		width = 80
	}
	height := m.Height
	if height <= 0 {
		height = 24
	}
	divider := SeparatorStyle.Render(strings.Repeat("─", width))
	// The list keeps its rows; the divider takes one; leave a one-line gap.
	previewH := max(3, height-lipgloss.Height(list)-2)

	m.TranscriptModal.SetSize(width, previewH)
	m.TranscriptModal.SetStatus("↑/↓ select · Enter to switch · x to stop · Esc to exit")
	m.TranscriptModal.SetLiveBadge(m.transcriptLiveBadge())

	return m.TranscriptModal.View() + "\n" + divider + "\n" + list
}

// rowPrefix is the 2-column gutter: a caret for the selected row, blanks else.
func rowPrefix(selected bool) string {
	if selected {
		return CommandStyle.Render("❯ ")
	}
	return "  "
}

// fleetDot is the status glyph: a filled ● for the selected row, a hollow ○
// otherwise. Run/ended state is conveyed by the row's "(ended)" suffix, so the
// dot is free to signal selection.
func fleetDot(selected bool) string {
	if selected {
		return ToolIconStyle.Render("● ")
	}
	return MutedStyle.Render("○ ")
}

// fleetName styles a row label — emphasised when selected, muted otherwise.
func fleetName(name string, selected bool) string {
	if selected {
		return ToolNameStyle.Render(name)
	}
	return MutedStyle.Render(name)
}

// fleetHint is the keybind line shown on the main row's right edge: the active
// actions while focused, a discovery cue while idle.
func (m *Model) fleetHint() string {
	if m.FleetFocus {
		h := "Enter to view"
		if m.config.StopAgent != nil {
			h += " · x to stop"
		}
		h += " · Esc to exit"
		return MutedStyle.Italic(true).Render(h)
	}
	return MutedStyle.Italic(true).Render("↓ to inspect")
}

// fleetRow lays out one row with left content and right-aligned content padded
// to width. lipgloss.Width measures display columns (ANSI-aware), so styled
// text aligns correctly. Falls back to a single space when content overflows.
func fleetRow(left, right string, width int) string {
	if right == "" {
		return left
	}
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}
