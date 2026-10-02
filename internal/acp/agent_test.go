package acp

import (
	"errors"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"
)

func TestTurnResultMapsTheRunsEnd(t *testing.T) {
	end := func(r agentcore.EndReason) *agentcore.RunEnd { return &agentcore.RunEnd{Reason: r} }
	tests := []struct {
		name string
		run  *agentcore.RunEnd
		want acp.StopReason
	}{
		{"no run yet", nil, acp.StopReasonEndTurn},
		{"stop", end(agentcore.EndDone), acp.StopReasonEndTurn},
		{"max turns", end(agentcore.EndMaxTurns), acp.StopReasonMaxTurnRequests},
		{"aborted", end(agentcore.EndAborted), acp.StopReasonCancelled},
	}
	for _, tt := range tests {
		resp, err := turnResult(tt.run, nil)
		if err != nil || resp.StopReason != tt.want {
			t.Errorf("%s: got (%q, %v), want %q", tt.name, resp.StopReason, err, tt.want)
		}
	}
}

// A failed run is a failed prompt, carrying the run's error when there is one.
func TestTurnResultFailsAnErroredRun(t *testing.T) {
	run := &agentcore.RunEnd{Reason: agentcore.EndError}
	if _, err := turnResult(run, errors.New("rate limited")); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want the run's error", err)
	}
	if _, err := turnResult(run, nil); err == nil {
		t.Fatal("an errored run without a recorded error still fails the prompt")
	}
}
