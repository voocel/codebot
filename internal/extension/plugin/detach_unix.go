//go:build !windows

package plugin

import (
	"os/exec"
	"syscall"
)

// detach runs cmd in a session of its own, with no terminal: ssh, as the
// user set it up, cannot ask for a passphrase or a host key there, so it
// fails rather than prompt.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
