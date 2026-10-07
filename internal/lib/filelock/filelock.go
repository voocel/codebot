// Package filelock locks a file across processes, so that concurrent codebot
// sessions can read-modify-write shared files in the user's home.
package filelock

import "os"

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
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
