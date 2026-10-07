//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// The lock is per handle, so two opens in one process exclude each other
// just like two processes do.
func lock(f *os.File) error {
	const all = ^uint32(0)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, all, all, new(windows.Overlapped))
}
