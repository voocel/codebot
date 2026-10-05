package acp

import (
	"context"
	"encoding/json"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/session"
)

// editSnapshot pairs a write/edit target's path with its pre-execution
// snapshot, so the call can be rendered as a native ACP diff once it completes.
// Captured at ToolStart (which comes before the tool runs) and consumed at
// ToolEnd.
type editSnapshot struct {
	path string
	old  diffSnapshot
}

// onEvent translates App events into ACP session/update notifications.
func (s *Server) onEvent(ev app.Event) {
	switch ev.Kind {
	case app.ModeChanged:
		s.send(acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{CurrentModeId: acp.SessionModeId(ev.Mode)}})
	case app.SessionEvent:
		if ev.Session.Kind == session.Agent {
			s.onAgentEvent(ev.Session.Agent)
		}
	}
}

func (s *Server) onAgentEvent(ev agentcore.Event) {
	switch e := ev.(type) {
	case agentcore.MessageDelta:
		// Tool argument deltas are not forwarded: StartToolCall carries the
		// whole input.
		switch d := e.Event.(type) {
		case litellm.ReasoningDelta:
			s.send(acp.UpdateAgentThoughtText(d.Text))
		case litellm.TextDelta:
			s.send(acp.UpdateAgentMessageText(d.Text))
		}
	case agentcore.ToolStart:
		title := e.Call.Name
		if e.Call.Tool != nil && e.Call.Tool.Label != "" {
			title = e.Call.Tool.Label
		}
		s.send(acp.StartToolCall(
			acp.ToolCallId(e.Call.ID), title,
			acp.WithStartKind(toolKind(e.Call.Name)),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(rawJSON(e.Call.Args)),
		))
		s.snapshotForDiff(e.Call, s.app.Current().Cwd())
	case agentcore.ToolEnd:
		status := acp.ToolCallStatusCompleted
		if e.Result.IsError {
			status = acp.ToolCallStatusFailed
		}
		opts := []acp.ToolCallUpdateOpt{
			acp.WithUpdateStatus(status),
			acp.WithUpdateRawOutput(e.Result.Text()),
		}
		if content, ok := s.diffContent(e); ok {
			opts = append(opts, acp.WithUpdateContent(content))
		}
		s.send(acp.UpdateToolCall(acp.ToolCallId(e.Call.ID), opts...))
	}
}

// snapshotForDiff captures a write/edit target's content before the tool runs,
// so its ToolEnd can emit it as a native ACP diff. It uses textForDiff
// (not ReadFile) so a disk copy is never mistaken for the editor buffer: an
// unreliable snapshot later suppresses the diff rather than rendering a
// misleading one.
func (s *Server) snapshotForDiff(call agentcore.ToolCall, cwd string) {
	if call.Name != "write" && call.Name != "edit" {
		return
	}
	path := editPath(call.Args, cwd)
	if path == "" {
		return
	}
	snap := editSnapshot{path: path, old: s.fs.textForDiff(context.Background(), path)}
	s.mu.Lock()
	s.pendingEdits[acp.ToolCallId(call.ID)] = snap
	s.mu.Unlock()
}

// diffContent builds the native diff for a completed write/edit by pairing the
// pre-exec snapshot with the file's current content. Returns ok=false when there
// is no snapshot or the call failed; buildDiff drops the rest (unreliable
// snapshot, file gone, no change).
//
// The delete here is the only cleanup pendingEdits needs: agentcore emits a
// ToolEnd for every ToolStart, cancellation included, so every snapshot is
// reclaimed by its own end event — no turn-level sweep, which would risk
// dropping a still-in-flight snapshot.
func (s *Server) diffContent(ev agentcore.ToolEnd) ([]acp.ToolCallContent, bool) {
	id := acp.ToolCallId(ev.Call.ID)
	s.mu.Lock()
	snap, ok := s.pendingEdits[id]
	delete(s.pendingEdits, id)
	s.mu.Unlock()
	if !ok || ev.Result.IsError {
		return nil, false
	}
	cur := s.fs.textForDiff(context.Background(), snap.path)
	return buildDiff(snap.path, snap.old, cur)
}

// buildDiff turns a before/after snapshot pair into native diff content, or
// (nil,false) when a diff would be misleading or empty: either side unreliable,
// the file gone after the write, or no actual change. A reliable old snapshot
// that does not exist means a new file (no oldText).
func buildDiff(path string, old, cur diffSnapshot) ([]acp.ToolCallContent, bool) {
	if !old.reliable || !cur.reliable || !cur.exists {
		return nil, false
	}
	if old.exists && old.text == cur.text {
		return nil, false
	}
	if old.exists {
		return []acp.ToolCallContent{acp.ToolDiffContent(path, cur.text, old.text)}, true
	}
	return []acp.ToolCallContent{acp.ToolDiffContent(path, cur.text)}, true
}

// editPath resolves a tool call's file_path argument to an absolute path using
// the same rule the tools use, or "" when there is no usable path.
func editPath(args json.RawMessage, cwd string) string {
	var p struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.FilePath == "" {
		return ""
	}
	return agentcoretools.ResolvePath(cwd, p.FilePath)
}

func (s *Server) send(u acp.SessionUpdate) {
	_ = s.conn.Load().SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: s.sessionID(),
		Update:    u,
	})
}

func toolKind(name string) acp.ToolKind {
	switch name {
	case "read":
		return acp.ToolKindRead
	case "write", "edit":
		return acp.ToolKindEdit
	case "bash":
		return acp.ToolKindExecute
	case "grep", "glob", "ls":
		return acp.ToolKindSearch
	case "web_search", "web_fetch":
		return acp.ToolKindFetch
	default:
		return acp.ToolKindOther
	}
}

// rawJSON forwards a tool call's arguments as the ACP rawInput value, or nil
// when empty.
func rawJSON(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return r
}
