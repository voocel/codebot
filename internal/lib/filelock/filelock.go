// Package filelock locks a file across processes, so that concurrent codebot
// sessions can read-modify-write shared files in the user's home.
package filelock

import (
	"errors"
	"os"
)

// ErrLocked is TryLock's error when another holder has the lock.
var ErrLocked = errors.New("locked by another holder")

// Lock creates path if needed and blocks while another holder, in this
// process or another, has it. Process exit also releases it.
//
// Lock a dedicated lock file, not the guarded file itself: a file replaced
// by rename is a new file each time.
func Lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f, true); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// TryLock locks the open file f until it is closed, or returns ErrLocked
// at once if another holder has it. Others may still read and write f.
func TryLock(f *os.File) error { return lock(f, false) }
