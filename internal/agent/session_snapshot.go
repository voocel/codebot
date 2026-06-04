package agent

import "github.com/voocel/codebot/internal/config"

// Snapshotter captures and restores workspace file checkpoints for /undo.
// Implemented by internal/snapshot.Tracker and injected at assembly time, so
// the agent package stays decoupled from the git-shadow implementation.
type Snapshotter interface {
	// Track records a checkpoint of the current workspace. changed is false
	// when nothing changed since the last checkpoint.
	Track() (changed bool, err error)
	// Undo reverts the workspace to the most recent checkpoint. ok is false
	// when there is nothing to undo; changed lists the affected paths.
	Undo() (changed []string, ok bool, err error)
	// Rebind repoints the tracker at a session's persisted undo stack: it drops
	// the in-memory stack and loads whatever statePath holds. Called on session
	// switch/new so each session gets its own checkpoint history.
	Rebind(statePath string)
}

// undoStatePath returns the sidecar file persisting this session's undo stack,
// under the per-session dir alongside bg/ and tool-outputs/.
//
// Caller must hold s.mu. It reads s.store directly (Store.Header has its own
// lock) instead of s.SessionID(), which locks s.mu and would self-deadlock.
func (s *Session) undoStatePath() string {
	return config.UndoStatePath(s.cwd, s.store.Header().SessionID)
}

// snapshotTurnStart records a workspace checkpoint at the start of a turn.
// Best-effort and run OUTSIDE s.mu — git I/O must never hold the session lock;
// the tracker has its own lock. Failures are ignored so snapshotting never
// blocks a turn.
func (s *Session) snapshotTurnStart() {
	if s.snapshotter == nil {
		return
	}
	_, _ = s.snapshotter.Track()
}

// Undo reverts workspace files to the start of the most recent turn that
// changed files, leaving conversation history untouched. ok is false when
// there is nothing to undo (no tracker, or no recorded changes).
func (s *Session) Undo() (changed []string, ok bool, err error) {
	if s.snapshotter == nil {
		return nil, false, nil
	}
	return s.snapshotter.Undo()
}
