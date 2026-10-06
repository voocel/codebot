// Package regular reads files that must be regular: a repository may hold a
// symlink to a device or a named pipe where a file is looked for, which
// would read forever, or never.
package regular

import (
	"fmt"
	"os"
)

// ReadFile reads the file at path, following symlinks, if it is a regular
// file.
func ReadFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}
