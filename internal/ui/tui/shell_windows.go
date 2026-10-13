//go:build windows

package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// shellCommand runs line in cmd; stopping it kills what it started too.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd", "/C", line)
	cmd.Cancel = func() error {
		taskkill := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
		return exec.Command(taskkill, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	return cmd
}
