//go:build windows

package cron

import "syscall"

const processQueryLimitedInformation = 0x1000

// processAlive checks whether the PID still exists on Windows.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return true
	}

	// Access denied still means a process with that PID exists.
	return err == syscall.ERROR_ACCESS_DENIED
}
