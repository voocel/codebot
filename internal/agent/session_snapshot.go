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
	// Redo re-applies the most recently undone change. ok is false when there
	// is nothing to redo (no prior undo, or a new edit invalidated the branch).
	Redo() (changed []string, ok bool, err error)
	// DiffTop returns a numstat diff of what Undo would roll back, or "" when
	// there is nothing to undo.
	DiffTop() (string, error)
	// Rebind repoints the tracker at a session's persisted undo stack: it drops
	// the in-memory stack and loads whatever statePath holds. Called on session
	// switch/new so each session gets its own checkpoint history.
	Rebind(statePath string)
	// Close waits for any background snapshot maintenance to finish.
	Close()
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

// SnapshotEnabled reports whether workspace snapshots are active for this
// session. It is false when the workspace isn't a git repository (or the
// snapshot setting is off), in which case Undo/Redo/Diff are inert no-ops —
// callers use this to explain the no-op instead of reporting "nothing to do".
func (s *Session) SnapshotEnabled() bool {
	return s.snapshotter != nil
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

// Redo re-applies the most recently undone turn's file changes. ok is false
// when there is nothing to redo (no tracker, no prior undo, or the redo branch
// was invalidated by a new turn).
func (s *Session) Redo() (changed []string, ok bool, err error) {
	if s.snapshotter == nil {
		return nil, false, nil
	}
	return s.snapshotter.Redo()
}

// Diff returns a numstat preview of what Undo would roll back (the last turn's
// file changes), or "" when there is nothing to undo.
func (s *Session) Diff() (string, error) {
	if s.snapshotter == nil {
		return "", nil
	}
	return s.snapshotter.DiffTop()
}
