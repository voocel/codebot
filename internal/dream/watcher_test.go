package dream

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"

	"github.com/voocel/codebot/internal/config"
)

// observeCall feeds the watcher one complete tool round-trip: the assistant's
// call and the result it came back with.
func observeCall(t *testing.T, w *Watcher, id, name, path string, failed bool) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"file_path": path})
	if err != nil {
		t.Fatal(err)
	}
	w.observe(agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{{
			Type:     agentcore.ContentToolCall,
			ToolCall: &agentcore.ToolCall{ID: id, Name: name, Args: args},
		}},
	})
	w.observe(agentcore.Message{
		Role:     agentcore.RoleTool,
		Content:  []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: "done"}},
		Metadata: map[string]any{"tool_call_id": id, "is_error": failed},
	})
}

func TestWatcherCollectsWrittenMemories(t *testing.T) {
	t.Parallel()

	w := NewWatcher()
	observeCall(t, w, "1", "write", "/mem/patterns.md", false)
	observeCall(t, w, "2", "edit", "/mem/debugging.md", false)
	observeCall(t, w, "3", "edit", "/mem/patterns.md", false) // same file twice
	observeCall(t, w, "4", "read", "/mem/ignored.md", false)  // not a mutation

	if got, want := w.touched(), []string{"debugging.md", "patterns.md"}; !slices.Equal(got, want) {
		t.Fatalf("touched = %v, want %v", got, want)
	}
}

// A write can be rejected after the model asks for it — read-before-write
// validation and the path guard both do exactly that. Crediting the call
// rather than the result would announce memories the run never wrote.
func TestWatcherIgnoresRejectedWrites(t *testing.T) {
	t.Parallel()

	w := NewWatcher()
	observeCall(t, w, "1", "write", "/mem/outside-guard.md", true)
	observeCall(t, w, "2", "edit", "/mem/never-read.md", true)
	observeCall(t, w, "3", "write", "/mem/kept.md", false)

	if got, want := w.touched(), []string{"kept.md"}; !slices.Equal(got, want) {
		t.Fatalf("touched = %v, want %v", got, want)
	}
}

// A call with no result yet (the run died mid-turn) stays unreported.
func TestWatcherIgnoresUnansweredCall(t *testing.T) {
	t.Parallel()

	w := NewWatcher()
	args, _ := json.Marshal(map[string]string{"file_path": "/mem/pending.md"})
	w.observe(agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{{
			Type:     agentcore.ContentToolCall,
			ToolCall: &agentcore.ToolCall{ID: "1", Name: "write", Args: args},
		}},
	})

	if got := w.touched(); len(got) != 0 {
		t.Fatalf("touched = %v, want none", got)
	}
}

// The notice names what THIS run wrote. Carrying files over would credit a
// dream with work an earlier one did, every time after the first.
func TestWatcherResetsBetweenRuns(t *testing.T) {
	runner := &fakeRunner{output: "ok"}
	done := make(chan error, 2)
	w := NewWatcher()
	d := New(Config{
		MemoryDir:   t.TempDir(),
		SessionsDir: t.TempDir(),
		Settings:    config.DreamSettings{Enabled: true, MinHours: 24, MinSessions: 5},
		TaskRT:      task.NewRuntime(),
		Runner:      runner,
		Watcher:     w,
	})

	var seen [][]string
	d.SetOnDone(func(files []string, err error) {
		seen = append(seen, files)
		done <- err
	})

	observeCall(t, w, "1", "write", "first.md", false)
	if _, err := d.StartManual(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)

	if _, err := d.StartManual(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)

	if len(seen) != 2 {
		t.Fatalf("got %d completions, want 2", len(seen))
	}
	// The pre-run write is discarded by the reset, so both runs report nothing.
	for i, files := range seen {
		if len(files) != 0 {
			t.Fatalf("run %d reported %v, want no files", i, files)
		}
	}
}
