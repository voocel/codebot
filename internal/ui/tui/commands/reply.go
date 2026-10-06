package commands

import (
	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// Reply is a cell a command adds to the transcript. It goes under the
// command line To, after what the command added before, though the user
// may have sent more since.
type Reply struct {
	To   *transcript.Prompt
	Cell transcript.Cell
}

// Asked is a panel a command shows: what its choices add to the transcript
// goes under the command line To too.
type Asked struct {
	To    *transcript.Prompt
	Panel panel.Panel
}

// Under makes what cmd adds to the transcript, and the panels it shows,
// replies to the command line to.
func Under(to *transcript.Prompt, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch m := msg.(type) {
		case transcript.Cell:
			return Reply{to, m}
		case panel.Panel:
			return Asked{to, m}
		case tea.BatchMsg:
			return tea.BatchMsg(under(to, m))
		case steps:
			return tea.Sequence(under(to, m)...)()
		}
		return msg
	}
}

func under(to *transcript.Prompt, cmds []tea.Cmd) []tea.Cmd {
	out := make([]tea.Cmd, len(cmds))
	for i, c := range cmds {
		out[i] = Under(to, c)
	}
	return out
}

// steps run one after another. Under sees into them, as it cannot into
// tea.Sequence; they run under a command line alone.
type steps []tea.Cmd

// working notes text at once, then runs work: a command starting on
// something slow.
func working(text string, work tea.Cmd) tea.Cmd {
	return func() tea.Msg { return steps{note(text), work} }
}
