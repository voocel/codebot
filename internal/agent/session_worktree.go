package agent

import "github.com/voocel/codebot/internal/config"

// RetargetWorkspace moves the session to cwd: session cwd, snapshotter, and
// the two workspace-derived system blocks. The single primitive behind
// worktree enter AND exit.
//
// The cwd-bound tools (read/write/edit/bash/glob/grep/ls) are NOT rebuilt —
// they resolve paths against the cwd override the session threads onto every
// agent-loop context (see baseRunCtx), so one set of instances serves both the
// main repo and a worktree sandbox. That is also why this must be called at a
// turn boundary: baseRunCtx reads s.cwd when the next run starts.
func (s *Session) RetargetWorkspace(cwd string) {
	s.cwd.Store(&cwd)

	if s.deps.snapshotter != nil {
		sid := s.persist.currentStore().Header().SessionID
		s.deps.snapshotter.RebindWorkspace(config.SnapshotDir(cwd), cwd, config.UndoStatePath(cwd, sid))
	}

	files, skills := s.loadWorkspaceContext(cwd)
	s.prompt.installRetarget(cwd, files, skills)
}

// currentCwd is the race-safe read for goroutines that outlive a turn (memory
// extraction, settings persistence) — WaitForIdle does not wait for those, and
// RetargetWorkspace rewrites cwd underneath them.
func (s *Session) currentCwd() string {
	return *s.cwd.Load()
}
