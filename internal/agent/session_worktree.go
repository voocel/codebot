package agent

import "github.com/voocel/codebot/internal/config"

// RetargetWorkspace moves the session's filesystem surface to cwd: it updates
// the session cwd and repoints the snapshotter at the new workspace. The
// cwd-bound tools (read/write/edit/bash/glob/grep/ls) are NOT rebuilt — they
// resolve paths against the cwd override the session threads onto every
// agent-loop context (see Session.runCtx), so one set of tool instances serves
// both the main repo and a worktree sandbox. Every other tool is unaffected.
//
// This is the single primitive behind worktree enter AND exit — the caller
// passes the target cwd either way. Must be called at a turn boundary (session
// idle): it mutates s.cwd, which runCtx reads when the next run starts.
func (s *Session) RetargetWorkspace(cwd string) {
	s.mu.Lock()
	s.cwd = cwd
	sid := s.store.Header().SessionID
	s.mu.Unlock()

	if s.snapshotter != nil {
		s.snapshotter.RebindWorkspace(config.SnapshotDir(cwd), cwd, config.UndoStatePath(cwd, sid))
	}
}
