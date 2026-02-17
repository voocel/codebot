package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestOpenRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := Create(dir, "/workspace/project")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Fatalf("invalid session file")
	}

	var entry Entry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("unmarshal first entry: %v", err)
	}
	var h Header
	if err := json.Unmarshal(entry.Data, &h); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	h.Version = currentVersion - 1
	headerRaw, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	entry.Data = headerRaw
	lineRaw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal line: %v", err)
	}
	lines[0] = string(lineRaw)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write modified session: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatalf("expected open to fail for unsupported version")
	}
}
