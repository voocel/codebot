//go:build windows

package filelock

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

// The lock is per handle, so two opens in one process exclude each other
// just like two processes do. It covers one byte far past the end: Windows
// locks are mandatory, and one over the contents would keep others from
// reading them.
func lock(f *os.File, wait bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{Offset: math.MaxUint32, OffsetHigh: math.MaxInt32})
	if err == windows.ERROR_LOCK_VIOLATION {
		return ErrLocked
	}
	return err
}
