package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"

	"github.com/voocel/codebot/internal/agent/todo"
)

// NewTodoWrite stores nothing: the list lives in the history (see
// todo.FromHistory).
func NewTodoWrite() agentcore.Tool {
	item := schema.Object(
		schema.Property("content", schema.String("What needs to be done, in imperative form")).Required(),
		schema.Property("status", schema.Enum("Item status", string(todo.Pending), string(todo.InProgress), string(todo.Completed))).Required(),
	)
	return agentcore.Tool{
		Name:  todo.ToolName,
		Label: "Update Todos",
		Description: `Maintain a checklist for the current work. Each call replaces the whole list.

Use it when the work has three or more distinct steps, or the user gives several tasks at once. Skip it for a single, trivial, or purely conversational request.
- Mark an item in_progress before starting it, and keep exactly one in_progress while you work.
- Mark an item completed as soon as it is fully done. If it is blocked, partial, or still failing verification, keep it in_progress and add an item for what is blocking it.
- Drop items that are no longer relevant.`,
		Schema: schema.Object(
			schema.Property("todos", schema.Array("The complete updated list", item)).Required(),
		),
		Run: func(_ context.Context, args json.RawMessage) (agentcore.Result, error) {
			items, err := todo.Parse(args)
			if err != nil {
				return agentcore.Result{}, err
			}
			pending, inProgress, completed := todo.Counts(items)
			return agentcore.TextResult(fmt.Sprintf("Todo list updated: %d completed, %d in progress, %d pending.", completed, inProgress, pending)), nil
		},
	}
}
