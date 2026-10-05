package editor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	maxHistory = 500
	// maxPasted caps the paste bodies an entry keeps; past it they are
	// dropped and recall marks them unavailable.
	maxPasted = 1 << 20
	// maxHistoryLine bounds a line of the history file.
	maxHistoryLine = 8 << 20
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

func (h *History) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	var all []entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxHistoryLine)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Project != h.project || r.Display == "" {
			continue
		}
		all = append(all, entry{r.Display, r.Pasted})
	}
	seen := map[string]bool{}
	for _, e := range slices.Backward(all) {
		if seen[e.text] {
			continue
		}
		seen[e.text] = true
		h.items = append(h.items, e)
		if len(h.items) == maxHistory {
			break
		}
	}
}

func (h *History) append(r record) {
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}
