// Package detached starts the commands codebot runs on its own, such as
// hooks and git, apart from the terminal the TUI draws on.
package detached

import (
	"context"
	"os/exec"
	"time"
)

// Command is exec.CommandContext with the command in a session of its own,
// with no terminal: nothing it runs can prompt on the one the TUI draws on.
// Cancelled, the whole session is killed, what the command started too; on
// Windows, the command alone. Either way, Wait gives up on what still holds
// its output a second on.
func Command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	detach(cmd)
	cmd.WaitDelay = time.Second
	return cmd
}
