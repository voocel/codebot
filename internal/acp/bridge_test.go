package acp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/bootstrap"
)

func reliable(s string) diffSnapshot   { return diffSnapshot{text: s, exists: true, reliable: true} }
func reliableNew() diffSnapshot        { return diffSnapshot{reliable: true} } // exists=false
func unreliableSnapshot() diffSnapshot { return diffSnapshot{} }

func TestBuildDiff(t *testing.T) {
	const path = "/x.go"
	tests := []struct {
		name     string
		old, cur diffSnapshot
		want     bool // whether a diff is emitted
		newFile  bool // OldText should be nil
	}{
		{"normal change", reliable("a"), reliable("b"), true, false},
		{"new file", reliableNew(), reliable("b"), true, true},
		{"old unreliable suppresses diff", unreliableSnapshot(), reliable("b"), false, false},
		{"cur unreliable suppresses diff", reliable("a"), unreliableSnapshot(), false, false},
		{"file gone after write", reliable("a"), reliableNew(), false, false},
		{"no change", reliable("same"), reliable("same"), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, ok := buildDiff(path, tt.old, tt.cur)
			if ok != tt.want {
				t.Fatalf("emitted=%v want=%v", ok, tt.want)
			}
			if !ok {
				return
			}
			if len(content) != 1 || content[0].Diff == nil {
				t.Fatalf("expected one diff content, got %+v", content)
			}
			d := content[0].Diff
			if d.NewText != tt.cur.text {
				t.Fatalf("NewText=%q want=%q", d.NewText, tt.cur.text)
			}
			switch {
			case tt.newFile && d.OldText != nil:
				t.Fatalf("new file should have nil OldText, got %q", *d.OldText)
			case !tt.newFile && (d.OldText == nil || *d.OldText != tt.old.text):
				t.Fatalf("OldText mismatch: %v want=%q", d.OldText, tt.old.text)
			}
		})
	}
}

// A successful write/edit produces a ToolDiffContent pairing the pre-exec buffer
// with the post-exec buffer (codex review item: end-to-end diff emission).
func TestDiffContent_EmitsNativeDiff(t *testing.T) {
	dir := t.TempDir()
	bufs := []string{"old-buffer", "new-buffer"} // snapshot read, then post-exec read
	i := 0
	ws := &WorkspaceFS{
		conn: fakeConn{read: func(string) (string, error) {
			r := bufs[i]
			i++
			return r, nil
		}},
		sid:     "s",
		canRead: true,
	}
	a := &acpAgent{
		rt:           &bootstrap.Runtime{Cwd: dir},
		fs:           ws,
		pendingEdits: make(map[acp.ToolCallId]editSnapshot),
	}
	args := json.RawMessage(`{"file_path":"f.go","content":"new-buffer"}`)
	a.snapshotForDiff(&agentcore.Event{Tool: "write", ToolID: "t1", Args: args})

	content, ok := a.diffContent(&agentcore.Event{Tool: "write", ToolID: "t1", Args: args})
	if !ok || len(content) != 1 || content[0].Diff == nil {
		t.Fatalf("expected a native diff, got ok=%v content=%+v", ok, content)
	}
	d := content[0].Diff
	if d.OldText == nil || *d.OldText != "old-buffer" || d.NewText != "new-buffer" {
		t.Fatalf("diff old/new mismatch: old=%v new=%q", d.OldText, d.NewText)
	}
}

// A background SEError (an async worker failing a turn later) routes through
// finishTurn. finishTurn must not drop snapshots for tool calls still in
// flight — each is reclaimed by its own ToolExecEnd, not a sweep.
func TestFinishTurn_KeepsPendingEdits(t *testing.T) {
	a := &acpAgent{pendingEdits: map[acp.ToolCallId]editSnapshot{
		"t1": {path: "/x.go", old: reliable("a")},
	}}
	a.finishTurn(turnResult{stop: acp.StopReasonEndTurn})
	if _, ok := a.pendingEdits["t1"]; !ok {
		t.Fatal("finishTurn must not drop in-flight snapshots")
	}
}

// An unreliable pre-exec snapshot (editor error on an existing file) must
// suppress the diff rather than render one off the disk copy.
func TestDiffContent_SkipsWhenSnapshotUnreliable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("disk-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &WorkspaceFS{
		conn:    fakeConn{read: func(string) (string, error) { return "", errors.New("editor error") }},
		sid:     "s",
		canRead: true,
	}
	a := &acpAgent{
		rt:           &bootstrap.Runtime{Cwd: dir},
		fs:           ws,
		pendingEdits: make(map[acp.ToolCallId]editSnapshot),
	}
	args := json.RawMessage(`{"file_path":"f.go","content":"x"}`)
	a.snapshotForDiff(&agentcore.Event{Tool: "write", ToolID: "t1", Args: args})
	if _, ok := a.diffContent(&agentcore.Event{Tool: "write", ToolID: "t1", Args: args}); ok {
		t.Fatal("unreliable snapshot must suppress the diff")
	}
}
