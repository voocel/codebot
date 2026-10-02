// Package todo is the session's checklist. The model rewrites it whole with
// todo_write, so the current list is the last accepted call in the
// conversation and nothing is stored beside the history.
package todo

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// ToolName is the tool that writes the list.
const ToolName = "todo_write"

type Status string

const (
	Pending    Status = "pending"
	InProgress Status = "in_progress"
	Completed  Status = "completed"
)

type Item struct {
	Content string `json:"content"`
	Status  Status `json:"status"`
}

// Parse decodes todo_write arguments and enforces the list's invariants:
// every item has content and a known status, and at most one is in progress.
func Parse(args json.RawMessage) ([]Item, error) {
	var in struct {
		Todos []Item `json:"todos"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	inProgress := 0
	for i, it := range in.Todos {
		if strings.TrimSpace(it.Content) == "" {
			return nil, fmt.Errorf("todo %d has empty content", i+1)
		}
		switch it.Status {
		case Pending, Completed:
		case InProgress:
			inProgress++
		default:
			return nil, fmt.Errorf("todo %d has invalid status %q", i+1, it.Status)
		}
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("%d todos are in_progress; keep exactly one in progress at a time", inProgress)
	}
	return in.Todos, nil
}

// FromHistory returns the list set by the last successful todo_write call, or
// nil when there is none (never written, or summarized away by compaction).
func FromHistory(msgs []agentcore.Message) []Item {
	calls := make(map[string]json.RawMessage)
	var current []Item
	for _, msg := range msgs {
		switch msg.Role {
		case litellm.RoleAssistant:
			for _, tc := range msg.ToolCalls() {
				if tc.Name == ToolName {
					calls[tc.ID] = json.RawMessage(tc.Arguments)
				}
			}
		case litellm.RoleTool:
			result, _ := msg.ToolResult()
			args, ok := calls[result.ToolUseID]
			if !ok || result.IsError {
				continue
			}
			if items, err := Parse(args); err == nil {
				current = items
			}
		}
	}
	return current
}

// Counts tallies the list by status.
func Counts(items []Item) (pending, inProgress, completed int) {
	for _, it := range items {
		switch it.Status {
		case Pending:
			pending++
		case InProgress:
			inProgress++
		case Completed:
			completed++
		}
	}
	return pending, inProgress, completed
}
