// Package regular reads files that must be regular: a repository may hold a
// symlink to a device or a named pipe where a file is looked for, which
// would read forever, or never. Or to a file of the user's outside it,
// which is to stay theirs: see Within.
package regular

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Within returns path with symlinks resolved, failing where it leads
// outside root, symlinks resolved too.
func Within(root, path string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%s leads outside %s", path, root)
	}
	return real, nil
}

// ReadFileIn reads the file at path, if it is a regular file within root.
func ReadFileIn(root, path string) ([]byte, error) {
	real, err := Within(root, path)
	if err != nil {
		return nil, err
	}
	return ReadFile(real)
}
