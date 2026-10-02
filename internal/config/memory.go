package config

import (
	"os"
	"path/filepath"
)

// MemoryDir returns ~/.codebot/projects/<projectID>/memory/.
func MemoryDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "projects", projectID(cwd), "memory")
}

// MemoryFilePath returns the path to MEMORY.md.
func MemoryFilePath(cwd string) string {
	return filepath.Join(MemoryDir(cwd), "MEMORY.md")
}

// EnsureMemoryDir creates the memory directory if it doesn't exist.
func EnsureMemoryDir(cwd string) {
	_ = os.MkdirAll(MemoryDir(cwd), 0o755)
}
