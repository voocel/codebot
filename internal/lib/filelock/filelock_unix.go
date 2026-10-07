//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// flock is per open file, so two opens in one process exclude each other
// just like two processes do.
func lock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}
