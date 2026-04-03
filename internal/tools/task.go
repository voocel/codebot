package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

// ---------------------------------------------------------------------------
// TaskListTool — list background tasks
// ---------------------------------------------------------------------------

// TaskListTool lists background shell and subagent tasks.
type TaskListTool struct{ rt *agentcore.TaskRuntime }

func (t *TaskListTool) Name() string  { return "task_list" }
func (t *TaskListTool) Label() string { return "List Background Tasks" }
func (t *TaskListTool) Description() string {
	return "List background shell and subagent tasks."
}
func (t *TaskListTool) Schema() map[string]any { return schema.Object() }
func (t *TaskListTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	entries := t.rt.List()
	if len(entries) == 0 {
		return json.Marshal(map[string]any{"tasks": []any{}})
	}
	tasks := make([]map[string]any, 0, len(entries))
	for i := range entries {
		tasks = append(tasks, formatTaskEntry(&entries[i], "", false))
	}
	return json.Marshal(map[string]any{"tasks": tasks})
}

// ---------------------------------------------------------------------------
// TaskOutputTool — read/wait for background task output
// ---------------------------------------------------------------------------

// TaskOutputTool lets the model query or wait for background task output.
type TaskOutputTool struct{ rt *agentcore.TaskRuntime }

func (t *TaskOutputTool) Name() string  { return "task_output" }
func (t *TaskOutputTool) Label() string { return "Get Task Output" }
func (t *TaskOutputTool) Description() string {
	return "Read the output of a background task. Use block=true to wait for completion."
}
func (t *TaskOutputTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("task_id", schema.String("Background task ID")).Required(),
		schema.Property("block", schema.Bool("Wait for task completion before returning")),
		schema.Property("timeout", schema.Int("Max seconds to wait when block=true")),
	)
}
func (t *TaskOutputTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		TaskID  string `json:"task_id"`
		Block   bool   `json:"block"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	entry := t.rt.Get(a.TaskID)
	if entry == nil {
		return json.Marshal(fmt.Sprintf("Task not found: %s", a.TaskID))
	}
	if !a.Block || entry.Status != agentcore.TaskRunning {
		return json.Marshal(formatTaskEntry(entry, readTaskTail(entry.OutputFile, 32*1024), true))
	}

	timeout := 120
	if a.Timeout > 0 {
		timeout = a.Timeout
	}
	deadline := time.NewTimer(time.Duration(timeout) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return json.Marshal("Cancelled")
		case <-deadline.C:
			entry = t.rt.Get(a.TaskID)
			if entry == nil {
				return json.Marshal(fmt.Sprintf("Task not found: %s", a.TaskID))
			}
			return json.Marshal(formatTaskEntry(entry, readTaskTail(entry.OutputFile, 32*1024), true))
		case <-ticker.C:
			entry = t.rt.Get(a.TaskID)
			if entry == nil {
				return json.Marshal(fmt.Sprintf("Task not found: %s", a.TaskID))
			}
			if entry.Status != agentcore.TaskRunning {
				return json.Marshal(formatTaskEntry(entry, readTaskTail(entry.OutputFile, 32*1024), true))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TaskStopTool — stop a background task
// ---------------------------------------------------------------------------

// TaskStopTool lets the model terminate a background task.
type TaskStopTool struct{ rt *agentcore.TaskRuntime }

func (t *TaskStopTool) Name() string  { return "task_stop" }
func (t *TaskStopTool) Label() string { return "Stop Background Task" }
func (t *TaskStopTool) Description() string {
	return "Stop a running background task by its ID."
}
func (t *TaskStopTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("task_id", schema.String("Background task ID")).Required(),
	)
}
func (t *TaskStopTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if t.rt.Stop(a.TaskID) {
		return json.Marshal(fmt.Sprintf("Stop signal sent to task %s", a.TaskID))
	}
	return json.Marshal(fmt.Sprintf("Task %s not found or already finished", a.TaskID))
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewTaskTools creates the three background task tools.
func NewTaskTools(rt *agentcore.TaskRuntime) []agentcore.Tool {
	return []agentcore.Tool{
		&TaskListTool{rt: rt},
		&TaskOutputTool{rt: rt},
		&TaskStopTool{rt: rt},
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func formatTaskEntry(entry *agentcore.BackgroundTaskEntry, output string, withVerification bool) map[string]any {
	result := map[string]any{
		"task_id":     entry.ID,
		"type":        entry.Type,
		"status":      entry.Status,
		"description": entry.Description,
	}
	if entry.OutputFile != "" {
		result["output_file"] = entry.OutputFile
	}
	if output != "" {
		result["output"] = output
	}
	if entry.Error != "" {
		result["error"] = entry.Error
	}
	if entry.Type == agentcore.TaskTypeShell {
		result["command"] = entry.Command
		result["pid"] = entry.PID
		if entry.Status != agentcore.TaskRunning {
			result["exit_code"] = entry.ExitCode
		}
	}
	if entry.Type == agentcore.TaskTypeSubAgent {
		result["agent"] = entry.Agent
		result["tool_count"] = entry.ToolCount
		result["tokens_in"] = entry.TokensIn
		result["tokens_out"] = entry.TokensOut
	}
	if withVerification && entry.Status != agentcore.TaskRunning {
		result["verification_needed"] = true
	}
	return result
}

func readTaskTail(path string, maxBytes int) string {
	if path == "" || maxBytes <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size == 0 {
		return ""
	}
	start := int64(0)
	if size > int64(maxBytes) {
		start = size - int64(maxBytes)
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return ""
	}
	text := string(buf)
	if start > 0 {
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
	}
	return strings.TrimSpace(text)
}
