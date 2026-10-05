//go:build !windows

package tui

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand runs line in sh, in a process group of its own, so that
// stopping it stops what it started too.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}
