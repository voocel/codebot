package editor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	maxHistory = 500 // entries kept of a project
	// maxPasted caps the paste bodies an entry keeps; past it they are
	// dropped and recall marks them unavailable.
	maxPasted = 1 << 20
	// maxHistoryFile bounds the history file. Past it, the file is rewritten
	// with the newest entries that fit in half of it, so that it is not
	// rewritten on every start.
	maxHistoryFile = 10 << 20
)

// History is what the user sent in a project, newest first, kept in a JSON
// lines file shared by every project.
type History struct {
	path      string
	project   string
	sessionID string
	items     []entry
}

type entry struct {
	text   string
	pasted map[int]string // the bodies of the paste references in text
}

type record struct {
	Display   string         `json:"display"`
	Pasted    map[int]string `json:"pastedContents"`
	Timestamp int64          `json:"timestamp"`
	Project   string         `json:"project"`
	SessionID string         `json:"sessionId,omitempty"`
}

// NewHistory reads the history of project from path.
func NewHistory(path, project string) *History {
	h := &History{path: path, project: project}
	h.load()
	return h
}

// SetSession tags what is added from now on with the conversation id.
func (h *History) SetSession(id string) { h.sessionID = id }

// Len is the number of entries.
func (h *History) Len() int { return len(h.items) }

// get returns entry i, 0 being the newest.
func (h *History) get(i int) entry { return h.items[i] }

// Add records text with the paste bodies it references.
func (h *History) Add(text string, pasted map[int]string) {
	if text == "" {
		return
	}
	total := 0
	for _, b := range pasted {
		total += len(b)
	}
	if total > maxPasted {
		dropped := make(map[int]string, len(pasted))
		for id := range pasted {
			dropped[id] = ""
		}
		pasted = dropped
	}
	h.items = slices.DeleteFunc(h.items, func(e entry) bool { return e.text == text })
	h.items = slices.Insert(h.items, 0, entry{text, pasted})
	if len(h.items) > maxHistory {
		h.items = h.items[:maxHistory]
	}
	h.append(record{Display: text, Pasted: pasted, Timestamp: time.Now().UnixMilli(), Project: h.project, SessionID: h.sessionID})
}

// stored is an entry of the history file, as read.
type stored struct {
	raw []byte
	record
}

func (h *History) load() {
	data, err := os.ReadFile(h.path)
	if err != nil {
		return
	}
	var lines []stored
	for _, raw := range bytes.Split(data, []byte("\n")) {
		var r record
		if json.Unmarshal(raw, &r) == nil && r.Display != "" {
			lines = append(lines, stored{raw, r})
		}
	}
	seen := map[string]bool{}
	for _, l := range slices.Backward(lines) {
		if l.Project != h.project || seen[l.Display] {
			continue
		}
		seen[l.Display] = true
		h.items = append(h.items, entry{l.Display, l.Pasted})
		if len(h.items) == maxHistory {
			break
		}
	}
	if len(data) > maxHistoryFile {
		h.compact(lines)
	}
}

// compact rewrites the history file with the newest entries of each project
// that fit in half of maxHistoryFile. Another codebot appending meanwhile
// may lose its entry, which is only history.
func (h *History) compact(lines []stored) {
	type key struct{ project, text string }
	seen := map[key]bool{}
	kept := map[string]int{}
	budget := maxHistoryFile / 2
	var keep [][]byte
	for _, l := range slices.Backward(lines) {
		k := key{l.Project, l.Display}
		if seen[k] || kept[l.Project] == maxHistory {
			continue
		}
		if budget -= len(l.raw) + 1; budget < 0 {
			break
		}
		seen[k] = true
		kept[l.Project]++
		keep = append(keep, l.raw)
	}
	var b bytes.Buffer
	for _, raw := range slices.Backward(keep) {
		b.Write(raw)
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), "history-*.jsonl")
	if err != nil {
		return
	}
	_, err = tmp.Write(b.Bytes())
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), h.path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
}

func (h *History) append(r record) {
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
	// What the user typed is theirs alone.
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}
