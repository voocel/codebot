//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// lock locks the whole file: one lock per handle, so two opens of one
// process exclude each other as two processes do. Closing the handle
// releases it.
func lock(f *os.File) error {
	const all = ^uint32(0)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, all, all, new(windows.Overlapped))
}
