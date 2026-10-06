package acp

import (
	"errors"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"
)

// The run's end maps to the stop reason the editor shows; a failed run is a
// failed prompt, carrying the run's error.
func TestTurnResultMapsTheRunsEnd(t *testing.T) {
	end := func(r agentcore.EndReason) *agentcore.RunEnd { return &agentcore.RunEnd{Reason: r} }
	tests := []struct {
		name    string
		run     *agentcore.RunEnd
		want    acp.StopReason
		wantErr string
	}{
		{"no run yet", nil, acp.StopReasonEndTurn, ""},
		{"stop", end(agentcore.EndDone), acp.StopReasonEndTurn, ""},
		{"max turns", end(agentcore.EndMaxTurns), acp.StopReasonMaxTurnRequests, ""},
		{"aborted", end(agentcore.EndAborted), acp.StopReasonCancelled, ""},
		{"failed", &agentcore.RunEnd{Reason: agentcore.EndError, Err: errors.New("rate limited")}, "", "rate limited"},
	}
	for _, tt := range tests {
		resp, err := turnResult(tt.run)
		if resp.StopReason != tt.want || (err == nil) != (tt.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
			t.Errorf("%s: got (%q, %v), want (%q, %q)", tt.name, resp.StopReason, err, tt.want, tt.wantErr)
		}
	}
}
