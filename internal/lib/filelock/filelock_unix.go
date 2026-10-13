//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// flock is per open file, so two opens in one process exclude each other
// just like two processes do.
func lock(f *os.File, wait bool) error {
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	for {
		switch err := syscall.Flock(int(f.Fd()), how); err {
		case syscall.EINTR:
		case syscall.EWOULDBLOCK:
			return ErrLocked
		default:
			return err
		}
	}
}
