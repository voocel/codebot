package acp

import (
	"encoding/json"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"
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

func TestDiffContent_EmitsNativeDiff(t *testing.T) {
	dir := t.TempDir()
	bufs := []string{"old-buffer", "new-buffer"} // snapshot read, then post-exec read
	i := 0
	ws := &EditorFS{
		conn: fakeConn{read: func(string) (string, error) {
			r := bufs[i]
			i++
			return r, nil
		}},
		sid:     "s",
		canRead: true,
	}
	s := &Server{fs: ws, pendingEdits: make(map[acp.ToolCallId]editSnapshot)}
	args := json.RawMessage(`{"file_path":"f.go","content":"new-buffer"}`)
	s.snapshotForDiff(agentcore.ToolCall{ID: "t1", Name: "write", Args: args}, dir)

	content, ok := s.diffContent(agentcore.ToolEnd{Call: agentcore.ToolCall{ID: "t1", Name: "write", Args: args}})
	if !ok || len(content) != 1 || content[0].Diff == nil {
		t.Fatalf("expected a native diff, got ok=%v content=%+v", ok, content)
	}
	d := content[0].Diff
	if d.OldText == nil || *d.OldText != "old-buffer" || d.NewText != "new-buffer" {
		t.Fatalf("diff old/new mismatch: old=%v new=%q", d.OldText, d.NewText)
	}
}
