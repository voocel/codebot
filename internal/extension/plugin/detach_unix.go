//go:build !windows

package plugin

import (
	"os/exec"
	"syscall"
)

// detach runs cmd in a session of its own, with no terminal: ssh, as the
// user set it up, cannot ask for a passphrase or a host key there, so it
// fails rather than prompt. Cancelled, the whole session is killed: git and
// its helpers, git-remote-https and ssh.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
