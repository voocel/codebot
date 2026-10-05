package todo

import (
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

func TestParseRejectsInvalidLists(t *testing.T) {
	t.Parallel()

	for name, args := range map[string]string{
		"empty content":    `{"todos":[{"content":" ","status":"pending"}]}`,
		"unknown status":   `{"todos":[{"content":"a","status":"done"}]}`,
		"two in progress":  `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`,
		"malformed object": `{"todos":"a"}`,
	} {
		if _, err := Parse(json.RawMessage(args)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func todoCall(id, args string) agentcore.Message {
	return agentcore.Message{
		Role:   litellm.RoleAssistant,
		Blocks: []litellm.Block{litellm.ToolUseBlock{ID: id, Name: ToolName, Arguments: args}},
	}
}

// The current list is the last call that succeeded; a rejected call leaves
// the previous one in force.
func TestFromHistoryTakesLastSuccessfulCall(t *testing.T) {
	t.Parallel()

	msgs := []agentcore.Message{
		todoCall("1", `{"todos":[{"content":"read","status":"in_progress"}]}`),
		agentcore.ToolResult("1", agentcore.TextResult("ok")),
		todoCall("2", `{"todos":[{"content":"read","status":"completed"},{"content":"edit","status":"in_progress"}]}`),
		agentcore.ToolResult("2", agentcore.TextResult("ok")),
		todoCall("3", `{"todos":[{"content":"x","status":"in_progress"},{"content":"y","status":"in_progress"}]}`),
		agentcore.ToolResult("3", agentcore.ErrorResult("rejected")),
	}
	got := FromHistory(msgs)
	if len(got) != 2 || got[1].Content != "edit" || got[1].Status != InProgress {
		t.Fatalf("FromHistory = %+v, want the list from call 2", got)
	}
}
