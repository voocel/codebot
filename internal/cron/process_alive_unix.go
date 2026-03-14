//go:build !windows

package cron

import "syscall"

// processAlive checks whether the PID still exists on Unix-like systems.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
