package editor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHistory writes n entries of each project, with a paste of size
// bytes, oldest first.
func writeHistory(t *testing.T, path string, n, size int, projects ...string) {
	t.Helper()
	var b bytes.Buffer
	for i := range n {
		for _, p := range projects {
			data, _ := json.Marshal(record{Display: fmt.Sprintf("%s %d", p, i), Pasted: map[int]string{1: strings.Repeat("x", size)}, Project: p})
			b.Write(append(data, '\n'))
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryCompactsAFileTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writeHistory(t, path, 60, 100<<10, "/a", "/b") // about 12 MiB
	h := NewHistory(path, "/a")
	if h.Len() != 60 || h.get(0).text != "/a 59" {
		t.Fatalf("read %d entries, the newest %q", h.Len(), h.get(0).text)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxHistoryFile/2 {
		t.Errorf("the file is %d bytes after compacting", info.Size())
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the file is %v", info.Mode().Perm())
	}

	// The newest of each project stay, in order.
	again := NewHistory(path, "/b")
	if again.Len() == 0 || again.get(0).text != "/b 59" || again.get(1).text != "/b 58" {
		t.Errorf("after compacting /b has %d entries, the newest %q", again.Len(), again.get(0).text)
	}
	if again.Len() >= 60 {
		t.Errorf("compacting kept all %d entries of /b", again.Len())
	}
}

func TestHistoryKeepsAFileSmallEnough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writeHistory(t, path, 3, 10, "/a")
	before, _ := os.ReadFile(path)
	NewHistory(path, "/a")
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("a small file was rewritten")
	}
}

func TestHistoryCapsEntriesPerProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writeHistory(t, path, 11, 1<<20, "/big") // the oldest, past the limit
	big, _ := os.ReadFile(path)
	writeHistory(t, path, maxHistory+100, 100, "/a")
	small, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(big, small...), 0o600); err != nil {
		t.Fatal(err)
	}

	NewHistory(path, "/a")
	data, _ := os.ReadFile(path)
	if n := bytes.Count(data, []byte(`"project":"/a"`)); n != maxHistory {
		t.Errorf("kept %d entries of /a, want %d", n, maxHistory)
	}
	if !bytes.Contains(data, []byte(`"project":"/big"`)) {
		t.Error("the room left was not given to older entries")
	}
}
