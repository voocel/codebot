package acp

import (
	"context"
	"encoding/json"
	"errors"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/agent"
)

// editSnapshot pairs a write/edit target's path with its pre-execution
// snapshot, so the call can be rendered as a native ACP diff once it completes.
// Captured at EventToolExecStart (which fires before the tool runs) and consumed
// at EventToolExecEnd.
type editSnapshot struct {
	path string
	old  diffSnapshot
}

// turnResult carries the outcome of one prompt turn back to Prompt. err is set
// for EndReasonError / SEError (ACP has no error stop reason).
type turnResult struct {
	stop acp.StopReason
	err  error
}

// onSessionEvent translates codebot session events into ACP session/update
// notifications and signals turn completion. Field names follow
// agentcore/event.go (Delta + DeltaKind, Result, IsError, Summary.EndReason).
func (a *acpAgent) onSessionEvent(ev agent.SessionEvent) {
	switch ev.Type {
	case agent.SEAgentEvent:
		if ev.AgentEvent != nil {
			a.onAgentEvent(ev.AgentEvent)
		}
	case agent.SEError:
		err := ev.Error
		if err == nil {
			err = errors.New("acp: session error")
		}
		a.finishTurn(turnResult{err: err})
	}
}

func (a *acpAgent) onAgentEvent(ev *agentcore.Event) {
	switch ev.Type {
	case agentcore.EventMessageUpdate:
		switch ev.DeltaKind {
		case agentcore.DeltaThinking:
			if ev.Delta != "" {
				a.send(acp.UpdateAgentThoughtText(ev.Delta))
			}
		case agentcore.DeltaToolCall:
			// Tool-argument delta stream; StartToolCall already carries the
			// full raw input, so nothing to forward here.
		default: // DeltaText
			if ev.Delta != "" {
				a.send(acp.UpdateAgentMessageText(ev.Delta))
			}
		}
	case agentcore.EventToolExecStart:
		title := ev.ToolLabel
		if title == "" {
			title = ev.Tool
		}
		a.send(acp.StartToolCall(
			acp.ToolCallId(ev.ToolID), title,
			acp.WithStartKind(toolKind(ev.Tool)),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(rawJSON(ev.Args)),
		))
		a.snapshotForDiff(ev)
	case agentcore.EventToolExecEnd:
		status := acp.ToolCallStatusCompleted
		if ev.IsError {
			status = acp.ToolCallStatusFailed
		}
		opts := []acp.ToolCallUpdateOpt{
			acp.WithUpdateStatus(status),
			acp.WithUpdateRawOutput(rawJSON(ev.Result)),
		}
		if content, ok := a.diffContent(ev); ok {
			opts = append(opts, acp.WithUpdateContent(content))
		}
		a.send(acp.UpdateToolCall(acp.ToolCallId(ev.ToolID), opts...))
	case agentcore.EventAgentEnd:
		a.finishTurn(endReasonResult(ev.Summary))
	}
}

// snapshotForDiff captures a write/edit target's content before the tool runs,
// so EventToolExecEnd can emit it as a native ACP diff. It uses textForDiff
// (not ReadFile) so a disk copy is never mistaken for the editor buffer: an
// unreliable snapshot later suppresses the diff rather than rendering a
// misleading one.
func (a *acpAgent) snapshotForDiff(ev *agentcore.Event) {
	if a.fs == nil || (ev.Tool != "write" && ev.Tool != "edit") {
		return
	}
	path := a.editPath(ev.Args)
	if path == "" {
		return
	}
	snap := editSnapshot{path: path, old: a.fs.textForDiff(context.Background(), path)}
	a.mu.Lock()
	a.pendingEdits[acp.ToolCallId(ev.ToolID)] = snap
	a.mu.Unlock()
}

// diffContent builds the native diff for a completed write/edit by pairing the
// pre-exec snapshot with the file's current content. Returns ok=false when there
// is no snapshot or the call failed; buildDiff drops the rest (unreliable
// snapshot, file gone, no change).
//
// The delete here is the only cleanup pendingEdits needs: agentcore emits a
// ToolExecEnd for every ToolExecStart (executeSingleToolCall covers each path,
// cancellation included), so every snapshot is reclaimed by its own end event —
// no turn-level sweep, which would risk dropping a still-in-flight snapshot.
func (a *acpAgent) diffContent(ev *agentcore.Event) ([]acp.ToolCallContent, bool) {
	id := acp.ToolCallId(ev.ToolID)
	a.mu.Lock()
	snap, ok := a.pendingEdits[id]
	delete(a.pendingEdits, id)
	a.mu.Unlock()
	if !ok || ev.IsError || a.fs == nil {
		return nil, false
	}
	cur := a.fs.textForDiff(context.Background(), snap.path)
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
func (a *acpAgent) editPath(args json.RawMessage) string {
	var p struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.FilePath == "" {
		return ""
	}
	return agentcoretools.ResolvePath(a.rt.Cwd, p.FilePath)
}

func (a *acpAgent) send(u acp.SessionUpdate) {
	_ = a.conn.Load().SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: a.sid,
		Update:    u,
	})
}

// finishTurn delivers a turn outcome to the waiting Prompt, if any. Non-blocking
// so a late/duplicate end event cannot stall the event loop.
func (a *acpAgent) finishTurn(r turnResult) {
	a.mu.Lock()
	ch := a.turn
	a.turn = nil
	a.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}

func endReasonResult(s *agentcore.RunSummary) turnResult {
	if s == nil {
		return turnResult{stop: acp.StopReasonEndTurn}
	}
	switch s.EndReason {
	case agentcore.EndReasonMaxTurns:
		return turnResult{stop: acp.StopReasonMaxTurnRequests}
	case agentcore.EndReasonAborted:
		return turnResult{stop: acp.StopReasonCancelled}
	case agentcore.EndReasonError:
		return turnResult{err: errors.New("acp: agent run ended with error")}
	default: // EndReasonStop
		return turnResult{stop: acp.StopReasonEndTurn}
	}
}

func toolKind(name string) acp.ToolKind {
	switch name {
	case "read":
		return acp.ToolKindRead
	case "write", "edit":
		return acp.ToolKindEdit
	case "bash":
		return acp.ToolKindExecute
	case "grep", "glob", "ls", "find":
		return acp.ToolKindSearch
	case "web_search", "web_fetch":
		return acp.ToolKindFetch
	default:
		return acp.ToolKindOther
	}
}

// rawJSON forwards agentcore's json.RawMessage (tool args/result) as the ACP
// rawInput/rawOutput value, or nil when empty.
func rawJSON(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return r
}
