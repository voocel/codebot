//go:build windows

package tui

import (
	"context"
	"os/exec"
)

// shellCommand runs line in cmd.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/C", line)
}
