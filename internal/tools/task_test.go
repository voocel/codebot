package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	coretask "github.com/voocel/agentcore/task"
	coreteam "github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/storage"
)

// TestTaskUpdate_AutoClaimOwnerOnInProgress: a teammate marking a task
// in_progress without explicit owner gets stamped as its owner. The leader
// path (no identity on ctx) must keep the existing leave-owner-empty
// behavior so leader-driven TodoWrite flows are not retroactively changed.
func TestTaskUpdate_AutoClaimOwnerOnInProgress(t *testing.T) {
	cases := []struct {
		name      string
		ctxSelf   string
		preOwner  string
		argOwner  *string
		argStatus *storage.TaskStatus
		wantOwner string
	}{
		{
			name:      "teammate claims unowned in_progress",
			ctxSelf:   "researcher",
			argStatus: ptrStatus(storage.TaskInProgress),
			wantOwner: "researcher",
		},
		{
			name:      "leader leaves owner empty",
			ctxSelf:   "",
			argStatus: ptrStatus(storage.TaskInProgress),
			wantOwner: "",
		},
		{
			name:      "explicit owner wins over auto-claim",
			ctxSelf:   "researcher",
			argOwner:  ptrString("planner"),
			argStatus: ptrStatus(storage.TaskInProgress),
			wantOwner: "planner",
		},
		{
			name:      "pre-existing owner not overwritten",
			ctxSelf:   "researcher",
			preOwner:  "planner",
			argStatus: ptrStatus(storage.TaskInProgress),
			wantOwner: "planner",
		},
		{
			name:      "completed status does not auto-claim",
			ctxSelf:   "researcher",
			argStatus: ptrStatus(storage.TaskCompleted),
			wantOwner: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewTaskStore()
			task := store.Create("do thing", "details", "doing thing", nil)
			if tc.preOwner != "" {
				if _, err := store.Update(task.ID, storage.TaskUpdateOpts{Owner: &tc.preOwner}); err != nil {
					t.Fatalf("seed owner: %v", err)
				}
			}
			tool := &TaskUpdateTool{store: store}

			args := map[string]any{"taskId": task.ID}
			if tc.argStatus != nil {
				args["status"] = string(*tc.argStatus)
			}
			if tc.argOwner != nil {
				args["owner"] = *tc.argOwner
			}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}

			ctx := context.Background()
			if tc.ctxSelf != "" {
				ctx = coreteam.WithIdentity(ctx, &coretask.Identity{AgentName: tc.ctxSelf})
			}
			out, err := tool.Execute(ctx, raw)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if strings.Contains(string(out), "\"error\"") {
				t.Fatalf("execute returned error result: %s", out)
			}

			updated, ok := store.Get(task.ID)
			if !ok {
				t.Fatalf("task vanished after update")
			}
			if updated.Owner != tc.wantOwner {
				t.Fatalf("owner = %q, want %q", updated.Owner, tc.wantOwner)
			}
		})
	}
}

func ptrStatus(s storage.TaskStatus) *storage.TaskStatus { return &s }
func ptrString(s string) *string                         { return &s }

// TestTaskUpdate_AssignmentNotifier: the tool layer fires the callback
// whenever the final owner differs from the existing one. Echo suppression
// (sender == new owner) is the notifier closure's job, not this layer's —
// keeping the tool ignorant of team.Registry lets the leader-only test path
// leave assigner nil without crashing.
func TestTaskUpdate_AssignmentNotifier(t *testing.T) {
	cases := []struct {
		name        string
		ctxSelf     string
		preOwner    string
		argOwner    *string
		argStatus   *storage.TaskStatus
		wantCalls   int
		wantToAgent string
	}{
		{
			name:        "explicit owner from empty fires once",
			argOwner:    ptrString("worker-a"),
			wantCalls:   1,
			wantToAgent: "worker-a",
		},
		{
			name:        "reassignment fires once",
			preOwner:    "worker-a",
			argOwner:    ptrString("worker-b"),
			wantCalls:   1,
			wantToAgent: "worker-b",
		},
		{
			name:      "same owner is a no-op",
			preOwner:  "worker-a",
			argOwner:  ptrString("worker-a"),
			wantCalls: 0,
		},
		{
			name:        "auto-claim also fires (echo skip is the closure's job)",
			ctxSelf:     "worker-c",
			argStatus:   ptrStatus(storage.TaskInProgress),
			wantCalls:   1,
			wantToAgent: "worker-c",
		},
		{
			name:      "no owner change, no fire",
			argStatus: ptrStatus(storage.TaskInProgress),
			wantCalls: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewTaskStore()
			created := store.Create("topic", "details", "doing topic", nil)
			if tc.preOwner != "" {
				if _, err := store.Update(created.ID, storage.TaskUpdateOpts{Owner: &tc.preOwner}); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}

			var (
				calls       int
				lastToAgent string
				lastPayload AssignmentPayload
			)
			tool := &TaskUpdateTool{store: store}
			tool.SetAssignmentNotifier(func(_ context.Context, to string, p AssignmentPayload) {
				calls++
				lastToAgent = to
				lastPayload = p
			})

			args := map[string]any{"taskId": created.ID}
			if tc.argStatus != nil {
				args["status"] = string(*tc.argStatus)
			}
			if tc.argOwner != nil {
				args["owner"] = *tc.argOwner
			}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			ctx := context.Background()
			if tc.ctxSelf != "" {
				ctx = coreteam.WithIdentity(ctx, &coretask.Identity{AgentName: tc.ctxSelf})
			}
			if _, err := tool.Execute(ctx, raw); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if calls != tc.wantCalls {
				t.Fatalf("notifier calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantCalls > 0 {
				if lastToAgent != tc.wantToAgent {
					t.Fatalf("notifier toAgent = %q, want %q", lastToAgent, tc.wantToAgent)
				}
				if lastPayload.TaskID != created.ID || lastPayload.Subject != "topic" {
					t.Fatalf("payload mismatch: %+v", lastPayload)
				}
			}
		})
	}
}
