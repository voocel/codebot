package tools

import "testing"

// A rejected list must surface as a tool error so the model retries and
// todo.FromHistory keeps the previous list in force.
func TestTodoWriteRejectsTwoInProgress(t *testing.T) {
	t.Parallel()

	args := `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`
	if _, err := call(t, NewTodoWrite(), args); err == nil {
		t.Fatal("expected an error for two in_progress items")
	}
}
