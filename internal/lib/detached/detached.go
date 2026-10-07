// Package detached runs background commands, such as hooks and git, away
// from the terminal the TUI draws on.
package detached

import (
	"context"
	"os/exec"
	"time"
)

// Command runs the command in its own session without a terminal, so nothing
// it starts can prompt on the TUI's terminal. Canceling kills the whole
// session, including child processes (on Windows, only the command itself).
// Wait stops waiting for leftover output holders after one second.
func Command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	detach(cmd)
	cmd.WaitDelay = time.Second
	return cmd
}
