package agent

import "github.com/voocel/codebot/internal/config"

// RetargetWorkspace moves the session's filesystem surface to cwd: it updates
// the session cwd and repoints the snapshotter at the new workspace. The
// cwd-bound tools (read/write/edit/bash/glob/grep/ls) are NOT rebuilt — they
// resolve paths against the cwd override the session threads onto every
// agent-loop context (see Session.baseRunCtx), so one set of tool instances serves
// both the main repo and a worktree sandbox. Every other tool is unaffected.
//
// This is the single primitive behind worktree enter AND exit — the caller
// passes the target cwd either way. Must be called at a turn boundary (session
// idle): it mutates s.cwd, which baseRunCtx reads when the next run starts.
func (s *Session) RetargetWorkspace(cwd string) {
	s.cwd.Store(&cwd)
	sid := s.persist.currentStore().Header().SessionID

	if s.deps.snapshotter != nil {
		s.deps.snapshotter.RebindWorkspace(config.SnapshotDir(cwd), cwd, config.UndoStatePath(cwd, sid))
	}
}

// currentCwd is the race-safe read for goroutines that outlive a turn (memory
// extraction, settings persistence) and for every tool call via baseRunCtx.
// RetargetWorkspace rewrites cwd on worktree enter/exit, and WaitForIdle does
// not wait for those goroutines.
func (s *Session) currentCwd() string {
	return *s.cwd.Load()
}
