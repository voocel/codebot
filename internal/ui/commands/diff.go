package commands

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/ui/tui"
)

// DiffCommand drives /diff — it previews the file changes /undo would roll back
// (the last turn's edits) as a per-file added/removed line summary.
type DiffCommand struct {
	session *agent.Session
}

// Diff constructs the /diff command.
func Diff(session *agent.Session) *DiffCommand {
	return &DiffCommand{session: session}
}

func (c *DiffCommand) Spec() Spec {
	return Spec{
		Name:        "diff",
		Usage:       "/diff",
		Description: "Preview the file changes /undo would roll back",
		Category:    "session",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *DiffCommand) Run(_ Invocation) tea.Cmd {
	if notice := snapshotUnavailable(c.session); notice != nil {
		return notice
	}
	numstat, err := c.session.Diff()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Diff failed: " + err.Error()))
	}
	numstat = strings.TrimSpace(numstat)
	if numstat == "" {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Nothing to preview — no file changes to undo."))
	}

	var b strings.Builder
	b.WriteString("/undo would roll back these changes:")
	var totalAdd, totalDel, files int
	for line := range strings.SplitSeq(numstat, "\n") {
		// numstat line: "<adds>\t<dels>\t<path>"; binary files report "-".
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		adds, dels, path := fields[0], fields[1], fields[2]
		files++
		if adds == "-" || dels == "-" {
			fmt.Fprintf(&b, "\n  %s  (binary)", path)
			continue
		}
		a, _ := strconv.Atoi(adds)
		d, _ := strconv.Atoi(dels)
		totalAdd += a
		totalDel += d
		fmt.Fprintf(&b, "\n  %s  +%s/-%s", path, adds, dels)
	}
	fmt.Fprintf(&b, "\n%d file(s), +%d/-%d", files, totalAdd, totalDel)
	return tui.SendCommandResult(tui.CommandStyle.Render(b.String()))
}
