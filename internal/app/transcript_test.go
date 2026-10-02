package app

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/voocel/codebot/internal/config"
)

func TestHiddenToolCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	memory := config.MemoryFilePath(t.TempDir())
	read := func(path string) json.RawMessage {
		args, _ := json.Marshal(map[string]string{"file_path": path})
		return args
	}
	tests := []struct {
		tool string
		args json.RawMessage
		want bool
	}{
		{"todo_write", nil, true},
		{"read", read(memory), true},
		{"read", read("~/.codebot/projects/p/memory/MEMORY.md"), true},
		{"read", read(filepath.Join(config.UserConfigDir(), "projects", "p", "notes.md")), false},
		{"read", read(filepath.Join(t.TempDir(), "memory", "MEMORY.md")), false},
		{"read", read("memory/MEMORY.md"), false},
		{"write", read(memory), false},
	}
	for _, tt := range tests {
		if got := HiddenToolCall(tt.tool, tt.args); got != tt.want {
			t.Errorf("HiddenToolCall(%s, %s) = %v, want %v", tt.tool, tt.args, got, tt.want)
		}
	}
}
