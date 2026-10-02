package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui"
)

// Worktree constructs /worktree — an isolated git-worktree sandbox the
// conversation works in, reviewed and merged or discarded on exit.
//
//	/worktree <name>   create a sandbox and move the conversation into it
//	/worktree exit     return to the workspace, keeping changes for review
//	/worktree discard  return to the workspace, discarding the sandbox
func Worktree(a *app.App) Command {
	return NewSimple(Spec{
		Name:        "worktree",
		Usage:       "/worktree <name> | exit | discard",
		Description: "Work in an isolated git worktree sandbox",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}, func(inv Invocation) tea.Cmd {
		conv := a.Current()
		arg := strings.TrimSpace(inv.RawArgs)
		var msg string
		switch arg {
		case "exit", "discard":
			res, err := conv.ExitWorktree(arg == "discard")
			if err != nil {
				return tui.SendCommandResult(tui.ErrorStyle.Render("Worktree: " + err.Error()))
			}
			msg = formatWorktreeExit(res)
		default:
			dir, err := conv.EnterWorktree(arg)
			if err != nil {
				return tui.SendCommandResult(tui.ErrorStyle.Render("Worktree: " + err.Error()))
			}
			msg = fmt.Sprintf("Entered worktree sandbox:\n  %s\nEdits here are isolated from the main workspace. /worktree exit to review, /worktree discard to drop.\nNote: /undo does not cross the worktree boundary.", dir)
		}
		return tui.SendCommandResult(tui.CommandStyle.Render(msg))
	})
}

func formatWorktreeExit(res app.WorktreeExit) string {
	switch {
	case res.Kept:
		return fmt.Sprintf(
			"Left worktree %q — changes kept for review:\n  %s (branch %s)\nReview/merge with git; remove with `git worktree remove %s` when done.",
			res.Slug, res.Dir, res.Branch, res.Dir)
	case res.HadChanges:
		return fmt.Sprintf("Left and discarded worktree %q (changes dropped).", res.Slug)
	case res.BranchKept:
		return fmt.Sprintf(
			"Left worktree %q — working tree was clean, but its branch %s has commits not merged elsewhere, so the branch was kept.\nInspect with `git log %s`; delete with `git branch -D %s` once merged.",
			res.Slug, res.Branch, res.Branch, res.Branch)
	default:
		return fmt.Sprintf("Left worktree %q — no changes, cleaned up.", res.Slug)
	}
}
