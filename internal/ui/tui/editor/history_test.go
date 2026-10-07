package editor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/codebot/internal/infra/config"
)

// TestMain uses a temporary HOME so the history locks, kept in the user's
// config directory, don't touch the real one.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "editor-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// writeHistory writes n entries per project, oldest first, each with a
// paste of size bytes.
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

// Entries another codebot appends during compaction survive: compact reads
// the file under the same lock appends take.
func TestHistoryCompactingKeepsWhatIsAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writeHistory(t, path, 60, 100<<10, "/a", "/b")
	unlock, err := config.LockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		NewHistory(path, "/a")
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	if info, _ := os.Stat(path); info.Size() <= maxHistoryFile {
		t.Fatal("compacted the file as another codebot appended to it")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(record{Display: "/b fresh", Project: "/b"})
	f.Write(append(data, '\n'))
	f.Close()
	unlock()
	<-done
	if info, _ := os.Stat(path); info.Size() > maxHistoryFile/2 {
		t.Errorf("the file is %d bytes after compacting", info.Size())
	}
	if h := NewHistory(path, "/b"); h.get(0).text != "/b fresh" {
		t.Errorf("the newest of /b is %q", h.get(0).text)
	}
}

// Adding waits for the lock compacting takes.
func TestHistoryAddWaitsForTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	h := NewHistory(path, "/a")
	unlock, err := config.LockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		h.Add("hello", nil)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("appended while another codebot held the lock")
	}
	unlock()
	<-done
	if again := NewHistory(path, "/a"); again.Len() != 1 || again.get(0).text != "hello" {
		t.Errorf("read %d entries", again.Len())
	}
}
