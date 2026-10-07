// Package regular reads only regular files. A repository may plant a symlink
// to a device or named pipe, which would block forever, or to one of the
// user's files outside it; see Within.
package regular

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

// Within resolves symlinks in both path and root and fails if path leads
// outside root.
func Within(root, path string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if rel, _ := filepath.Rel(realRoot, real); rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s leads outside %s", path, root)
	}
	return real, nil
}

func ReadFileIn(root, path string) ([]byte, error) {
	real, err := Within(root, path)
	if err != nil {
		return nil, err
	}
	return ReadFile(real)
}
