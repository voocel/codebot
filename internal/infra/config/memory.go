package config

import (
	"os"
	"path/filepath"
)

func MemoryDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "projects", projectID(cwd), "memory")
}

func MemoryFilePath(cwd string) string {
	return filepath.Join(MemoryDir(cwd), "MEMORY.md")
}

func EnsureMemoryDir(cwd string) {
	_ = os.MkdirAll(MemoryDir(cwd), 0o755)
}
