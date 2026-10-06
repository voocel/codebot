//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// lock takes an flock: one per open file, so two opens of one process
// exclude each other as two processes do. Closing the file releases it.
func lock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}
