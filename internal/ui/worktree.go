package ui

import (
	"fmt"

	"github.com/voocel/codebot/internal/bootstrap"
)

// wireWorktree binds the /worktree command's runtime callbacks onto the App,
// translating runtime results into user-facing messages. Called only for the
// interactive TUI; other frontends leave the callbacks nil and the command
// reports itself unavailable.
func (a *App) wireWorktree(rt *bootstrap.Runtime) {
	a.worktreeEnter = func(name string) (string, error) {
		dir, err := rt.EnterWorktree(name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"Entered worktree sandbox:\n  %s\nEdits here are isolated from the main workspace. /worktree exit to review, /worktree discard to drop.\nNote: /undo does not cross the worktree boundary.",
			dir), nil
	}
	a.worktreeExit = func(discard bool) (string, error) {
		res, err := rt.ExitWorktree(discard)
		if err != nil {
			return "", err
		}
		return formatWorktreeExit(res), nil
	}
	a.worktreeActive = rt.WorktreeActive
}

func formatWorktreeExit(res bootstrap.WorktreeExit) string {
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
