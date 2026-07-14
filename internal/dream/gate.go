package dream

import (
	"os"
	"strings"
	"time"
)

// countSessionsTouchedSince counts session transcripts (*.jsonl in the
// sessions directory) modified after since, excluding the current session.
// It reads mtimes only — parsing JSONL for content timestamps would cost a
// full scan per file. Undercounting is the safe direction: this is a
// skip-gate, not an accounting system.
func countSessionsTouchedSince(dir string, since time.Time, excludeID string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		// Session files are named <date>_<sessionID>.jsonl.
		if excludeID != "" && strings.HasSuffix(e.Name(), "_"+excludeID+".jsonl") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(since) {
			n++
		}
	}
	return n
}
