package dream

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"sync"

	"github.com/voocel/agentcore"
)

// Watcher records which memory files a run wrote to, so the completion notice
// can name them. Without it a dream leaves no trace the user will see — the
// task entry in /tasks is somewhere nobody looks.
//
// One Watcher is shared by the agent config and the Dreamer: runs are
// serialized by the lock and the running flag, so a single reset-then-collect
// cycle is enough.
type Watcher struct {
	mu      sync.Mutex
	pending map[string]string // tool_call_id → file name, awaiting its result
	files   map[string]struct{}
}

func NewWatcher() *Watcher {
	return &Watcher{pending: map[string]string{}, files: map[string]struct{}{}}
}

// observe pairs each write/edit call with its result and keeps the file only
// when that result succeeded. Bound to subagent.Config.OnMessage, which fires
// per appended message.
//
// The call alone is not evidence: read-before-write validation and the path
// guard both reject after the model has asked, so recording on the call would
// report memories a failed run never wrote.
func (w *Watcher) observe(msg agentcore.AgentMessage) {
	m, ok := msg.(agentcore.Message)
	if !ok {
		return
	}
	switch m.Role {
	case agentcore.RoleAssistant:
		for _, tc := range m.ToolCalls() {
			if tc.Name != "write" && tc.Name != "edit" {
				continue
			}
			var args struct {
				FilePath string `json:"file_path"`
			}
			if json.Unmarshal(tc.Args, &args) != nil || args.FilePath == "" {
				continue
			}
			w.mu.Lock()
			w.pending[tc.ID] = filepath.Base(args.FilePath)
			w.mu.Unlock()
		}
	case agentcore.RoleTool:
		id, _ := m.Metadata["tool_call_id"].(string)
		failed, _ := m.Metadata["is_error"].(bool)
		w.mu.Lock()
		if name, ok := w.pending[id]; ok {
			delete(w.pending, id)
			if !failed {
				w.files[name] = struct{}{}
			}
		}
		w.mu.Unlock()
	}
}

func (w *Watcher) reset() {
	w.mu.Lock()
	clear(w.pending)
	clear(w.files)
	w.mu.Unlock()
}

// touched returns the file names written this run, sorted for a stable notice.
func (w *Watcher) touched() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Sorted(maps.Keys(w.files))
}
