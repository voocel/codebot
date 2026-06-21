package acp

import (
	"context"
	"encoding/json"
	"errors"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/agent"
)

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
	case agentcore.EventToolExecEnd:
		status := acp.ToolCallStatusCompleted
		if ev.IsError {
			status = acp.ToolCallStatusFailed
		}
		a.send(acp.UpdateToolCall(
			acp.ToolCallId(ev.ToolID),
			acp.WithUpdateStatus(status),
			acp.WithUpdateRawOutput(rawJSON(ev.Result)),
		))
	case agentcore.EventAgentEnd:
		a.finishTurn(endReasonResult(ev.Summary))
	}
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
