// Package filelock locks a file across processes: codebot's sessions share
// the files in the user's home, and edit them read, change, write.
package filelock

import "os"

// Lock takes the lock on the file at path, which it creates if need be,
// waiting while another holds it, in this process or another. unlock
// releases it, as the process ending does.
//
// The lock is the file's own, not that of the file it guards: one written
// by renaming another over it is a new file each time.
func Lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
