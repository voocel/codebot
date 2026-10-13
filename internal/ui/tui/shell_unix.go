//go:build !windows

package tui

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand runs line in a session of its own: without the terminal, so
// that a program asking it for a password, as sudo does, fails rather than
// write over the screen and wait for input it cannot get; and as a process
// group, so that stopping it also kills its children.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}
