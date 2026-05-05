package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/permission"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/storage"
)

// ---------------------------------------------------------------------------
// TaskCreateTool
// ---------------------------------------------------------------------------

// TaskCreateTool creates new tasks.
type TaskCreateTool struct {
	store *storage.TaskStore
	hooks *hooks.Runner
}

func (t *TaskCreateTool) Name() string                           { return "task_create" }
func (t *TaskCreateTool) Label() string                          { return "Create Task" }
func (t *TaskCreateTool) ConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *TaskCreateTool) Description() string {
	return `Use this tool to create a structured task list for the current coding session. This helps track progress, organize complex work, and make progress visible to the user.

Use this tool proactively when:
- the task has 3 or more distinct steps
- the work is non-trivial and spans multiple operations, files, or phases
- the user explicitly asks for a todo list or gives multiple requirements
- you receive new complex instructions and should capture them as tasks before implementation

Do not use this tool when:
- there is only a single straightforward task
- the task is trivial and tracking it adds no value
- the request is purely conversational or informational

Task fields:
- subject: brief actionable title in imperative form
- description: what needs to be done
- activeForm: present continuous form shown while the task is in_progress

All new tasks start as pending. Break larger requests into multiple specific tasks instead of one broad task. Check task_list before creating more tasks to avoid duplicates. After creating tasks, use task_update to mark the next task in_progress and to set dependencies if needed.`
}
func (t *TaskCreateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("subject", schema.String("Brief imperative title (e.g. 'Fix authentication bug')")).Required(),
		schema.Property("description", schema.String("Detailed description of what needs to be done")).Required(),
		schema.Property("activeForm", schema.String("Present continuous form for progress display (e.g. 'Fixing authentication bug')")),
	)
}

// SetNotifyFn registers the TUI notification callback (delegates to store).
func (t *TaskCreateTool) SetNotifyFn(fn storage.TaskNotifyFn) { t.store.SetNotifyFn(fn) }

// Store returns the underlying TaskStore.
func (t *TaskCreateTool) Store() *storage.TaskStore { return t.store }

func (t *TaskCreateTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Subject     string `json:"subject"`
		Description string `json:"description"`
		ActiveForm  string `json:"activeForm"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Subject == "" {
		return json.Marshal("Validation error: subject is required")
	}
	if a.Description == "" {
		return json.Marshal("Validation error: description is required")
	}
	task := t.store.Create(a.Subject, a.Description, a.ActiveForm, nil)
	if t.hooks != nil {
		if err := t.hooks.RunTaskCreated(ctx, taskHookSnapshot(task)); err != nil {
			t.store.Delete(task.ID)
			return json.Marshal(fmt.Sprintf("Error: %s", err))
		}
	}
	return json.Marshal(map[string]any{
		"success": true,
		"task": map[string]any{
			"id":      task.ID,
			"subject": task.Subject,
		},
		"message": fmt.Sprintf("Created task #%s: %s", task.ID, task.Subject),
	})
}

// ---------------------------------------------------------------------------
// TaskGetTool
// ---------------------------------------------------------------------------

// TaskGetTool retrieves a single task by ID.
type TaskGetTool struct{ store *storage.TaskStore }

func (t *TaskGetTool) Name() string                           { return "task_get" }
func (t *TaskGetTool) Label() string                          { return "Get Task" }
func (t *TaskGetTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *TaskGetTool) ConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *TaskGetTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityRead}
}
func (t *TaskGetTool) Description() string {
	return `Use this tool to retrieve a task by ID from the task list.

Use this tool when:
- you need the full description and context before starting work
- you need to understand dependencies such as what blocks the task or what it blocks
- you were assigned a task and need the complete requirements before updating it

The result includes the task subject, description, status, and dependency details. Use task_list for summary view and task_get for full detail.`
}
func (t *TaskGetTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("taskId", schema.String("The task ID to retrieve")).Required(),
	)
}

func (t *TaskGetTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	task, ok := t.store.Get(a.TaskID)
	if !ok {
		return json.Marshal(fmt.Sprintf("Task %s not found", a.TaskID))
	}
	return json.Marshal(task)
}

// ---------------------------------------------------------------------------
// TaskUpdateTool
// ---------------------------------------------------------------------------

// TaskUpdateTool modifies an existing task.
type TaskUpdateTool struct {
	store *storage.TaskStore
	hooks *hooks.Runner
}

func (t *TaskUpdateTool) Name() string                           { return "task_update" }
func (t *TaskUpdateTool) Label() string                          { return "Update Task" }
func (t *TaskUpdateTool) ConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *TaskUpdateTool) Description() string {
	return `Use this tool to update a task in the task list.

Use this tool when:
- starting work on a task: mark it in_progress before you begin
- completing work on a task: mark it completed immediately after it is fully done
- a task is no longer needed: set status to deleted
- task details become clearer or dependencies need to be recorded

Task status workflow:
- pending -> in_progress -> completed
- use deleted to permanently remove a task

Rules:
- keep at most one task in_progress at a time
- IMPORTANT: always mark your current in_progress task completed before giving your final answer, unless the work is blocked, partial, or still failing verification
- do not batch multiple completions together
- after completing a task, call task_list to find the next available task
- if the work is blocked, partial, or failing verification, keep the task in_progress
- never mark a task completed if tests are failing, implementation is partial, or unresolved errors remain

Fields you can update:
- status
- subject
- description
- activeForm
- owner
- metadata
- addBlocks
- addBlockedBy

Read the latest task state with task_get before updating if there is any chance it changed since you last saw it.`
}
func (t *TaskUpdateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("taskId", schema.String("The task ID to update")).Required(),
		schema.Property("status", schema.Enum("New status", "pending", "in_progress", "completed", "deleted")),
		schema.Property("subject", schema.String("New subject")),
		schema.Property("description", schema.String("New description")),
		schema.Property("activeForm", schema.String("New activeForm for progress display")),
		schema.Property("owner", schema.String("Assign to an agent")),
		schema.Property("metadata", schema.Object()),
		schema.Property("addBlocks", schema.Array("Task IDs this task blocks", schema.String("task ID"))),
		schema.Property("addBlockedBy", schema.Array("Task IDs that block this task", schema.String("task ID"))),
	)
}

func (t *TaskUpdateTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		TaskID       string              `json:"taskId"`
		Status       *storage.TaskStatus `json:"status,omitempty"`
		Subject      *string             `json:"subject,omitempty"`
		Description  *string             `json:"description,omitempty"`
		ActiveForm   *string             `json:"activeForm,omitempty"`
		Owner        *string             `json:"owner,omitempty"`
		Metadata     map[string]any      `json:"metadata,omitempty"`
		AddBlocks    []string            `json:"addBlocks,omitempty"`
		AddBlockedBy []string            `json:"addBlockedBy,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.TaskID == "" {
		return json.Marshal("Validation error: taskId is required")
	}
	existing, ok := t.store.Get(a.TaskID)
	if !ok {
		return json.Marshal(fmt.Sprintf("Error: task %s not found", a.TaskID))
	}
	if t.hooks != nil && a.Status != nil && *a.Status == storage.TaskCompleted {
		if existing.Status != storage.TaskCompleted {
			next := previewTaskUpdate(existing, storage.TaskUpdateOpts{
				Status:       a.Status,
				Subject:      a.Subject,
				Description:  a.Description,
				ActiveForm:   a.ActiveForm,
				Owner:        a.Owner,
				Metadata:     a.Metadata,
				AddBlocks:    a.AddBlocks,
				AddBlockedBy: a.AddBlockedBy,
			})
			if err := t.hooks.RunTaskCompleted(ctx, taskHookSnapshot(existing), taskHookSnapshot(next)); err != nil {
				return json.Marshal(fmt.Sprintf("Error: %s", err))
			}
		}
	}
	updated, err := t.store.Update(a.TaskID, storage.TaskUpdateOpts{
		Status:       a.Status,
		Subject:      a.Subject,
		Description:  a.Description,
		ActiveForm:   a.ActiveForm,
		Owner:        a.Owner,
		Metadata:     a.Metadata,
		AddBlocks:    a.AddBlocks,
		AddBlockedBy: a.AddBlockedBy,
	})
	if err != nil {
		return json.Marshal(fmt.Sprintf("Error: %s", err))
	}
	if updated == nil {
		return json.Marshal(fmt.Sprintf("Task %s deleted", a.TaskID))
	}
	updatedFields := make([]string, 0, 8)
	statusChange := map[string]any(nil)
	if a.Status != nil {
		updatedFields = append(updatedFields, "status")
		statusChange = map[string]any{
			"from": string(existing.Status),
			"to":   string(updated.Status),
		}
	}
	if a.Subject != nil {
		updatedFields = append(updatedFields, "subject")
	}
	if a.Description != nil {
		updatedFields = append(updatedFields, "description")
	}
	if a.ActiveForm != nil {
		updatedFields = append(updatedFields, "activeForm")
	}
	if a.Owner != nil {
		updatedFields = append(updatedFields, "owner")
	}
	if len(a.Metadata) > 0 {
		updatedFields = append(updatedFields, "metadata")
	}
	if len(a.AddBlocks) > 0 {
		updatedFields = append(updatedFields, "blocks")
	}
	if len(a.AddBlockedBy) > 0 {
		updatedFields = append(updatedFields, "blockedBy")
	}
	message := fmt.Sprintf("Updated task #%s", updated.ID)
	if len(updatedFields) > 0 {
		message += ": " + strings.Join(updatedFields, ", ")
	}
	if statusChange != nil {
		message += fmt.Sprintf(" (%s -> %s)", statusChange["from"], statusChange["to"])
	}
	result := map[string]any{
		"success":        true,
		"id":             updated.ID,
		"status":         updated.Status,
		"updated_fields": updatedFields,
		"message":        message,
	}
	if statusChange != nil {
		result["status_change"] = statusChange
	}
	if updated.Status == storage.TaskCompleted {
		result["verification_needed"] = true
		result["hint"] = "Verify the result before proceeding to the next task."
	}
	return json.Marshal(result)
}

// ---------------------------------------------------------------------------
// TaskListTool
// ---------------------------------------------------------------------------

// TaskListTool lists all tasks.
type TaskListTool struct{ store *storage.TaskStore }

func (t *TaskListTool) Name() string                           { return "task_list" }
func (t *TaskListTool) Label() string                          { return "List Tasks" }
func (t *TaskListTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *TaskListTool) ConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *TaskListTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityRead}
}
func (t *TaskListTool) Description() string {
	return `Use this tool to list all tasks in the task list.

Use this tool when:
- checking overall progress
- looking for the next pending and unblocked task
- checking for newly unblocked work after completing a task
- reviewing blocked tasks and their dependencies
- checking what already exists before creating more tasks

The output summarizes each task with its id, subject, status, owner, and dependencies. When multiple tasks are available, prefer lower task IDs first because earlier tasks often unlock later work. Use task_get for full details on a specific task.`
}
func (t *TaskListTool) Schema() map[string]any { return schema.Object() }

func (t *TaskListTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	snap := t.store.Snapshot()
	if snap.Total == 0 {
		return json.Marshal("No tasks")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Tasks: %d total (%d pending, %d in_progress, %d completed)\n",
		snap.Total, snap.Pending, snap.InProgress, snap.Completed)

	items := prioritizeTaskSnapshotItems(snap)
	if current := currentInProgressTask(items); current != nil {
		fmt.Fprintf(&sb, "Current: #%s %s\n", current.ID, renderTaskSubject(*current))
	}
	if next := nextAvailableTask(items, snap); next != nil {
		fmt.Fprintf(&sb, "Next: #%s %s\n", next.ID, next.Subject)
	}

	for _, task := range items {
		line := fmt.Sprintf("- #%s [%s] %s", task.ID, task.Status, renderTaskSubject(task))
		if task.Owner != "" {
			line += fmt.Sprintf(" (owner: %s)", task.Owner)
		}
		if active := taskBlockedByOpenIDs(task, snap); len(active) > 0 {
			line += fmt.Sprintf(" [blocked by: %s]", strings.Join(active, ", "))
		}
		sb.WriteString(line + "\n")
	}
	return json.Marshal(strings.TrimRight(sb.String(), "\n"))
}

func prioritizeTaskSnapshotItems(snap storage.TaskSnapshot) []storage.Task {
	if len(snap.Items) == 0 {
		return nil
	}

	unresolved := make(map[string]struct{}, len(snap.Items))
	for _, task := range snap.Items {
		if task.Status != storage.TaskCompleted {
			unresolved[task.ID] = struct{}{}
		}
	}

	var inProgress []storage.Task
	var pending []storage.Task
	var completed []storage.Task
	for _, task := range snap.Items {
		switch task.Status {
		case storage.TaskInProgress:
			inProgress = append(inProgress, task)
		case storage.TaskPending:
			pending = append(pending, task)
		case storage.TaskCompleted:
			completed = append(completed, task)
		default:
			pending = append(pending, task)
		}
	}

	storage.SortTasksByID(inProgress)
	sort.Slice(pending, func(i, j int) bool {
		aBlocked := taskHasOpenBlockers(pending[i], unresolved)
		bBlocked := taskHasOpenBlockers(pending[j], unresolved)
		if aBlocked != bBlocked {
			return !aBlocked
		}
		return storage.CompareTaskIDs(pending[i].ID, pending[j].ID) < 0
	})
	storage.SortTasksByID(completed)

	out := make([]storage.Task, 0, len(snap.Items))
	out = append(out, inProgress...)
	out = append(out, pending...)
	out = append(out, completed...)
	return out
}

func currentInProgressTask(tasks []storage.Task) *storage.Task {
	for i := range tasks {
		if tasks[i].Status == storage.TaskInProgress {
			return &tasks[i]
		}
	}
	return nil
}

func nextAvailableTask(tasks []storage.Task, snap storage.TaskSnapshot) *storage.Task {
	for i := range tasks {
		if tasks[i].Status != storage.TaskPending {
			continue
		}
		if len(taskBlockedByOpenIDs(tasks[i], snap)) == 0 {
			return &tasks[i]
		}
	}
	return nil
}

func renderTaskSubject(task storage.Task) string {
	if task.Status == storage.TaskInProgress && task.ActiveForm != "" {
		return task.ActiveForm
	}
	return task.Subject
}

func taskBlockedByOpenIDs(task storage.Task, snap storage.TaskSnapshot) []string {
	if len(task.BlockedBy) == 0 {
		return nil
	}
	statusByID := make(map[string]storage.TaskStatus, len(snap.Items))
	for _, item := range snap.Items {
		statusByID[item.ID] = item.Status
	}
	var active []string
	for _, id := range task.BlockedBy {
		if statusByID[id] != storage.TaskCompleted {
			active = append(active, "#"+id)
		}
	}
	sort.Strings(active)
	return active
}

func taskHasOpenBlockers(task storage.Task, unresolved map[string]struct{}) bool {
	for _, id := range task.BlockedBy {
		if _, ok := unresolved[id]; ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// TaskOutputTool — read/wait for background task output
// ---------------------------------------------------------------------------

// TaskOutputTool lets the model query or wait for background task output.
type TaskOutputTool struct{ rt *task.Runtime }

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
	if !a.Block || entry.Status != task.Running {
		return json.Marshal(formatBgEntry(entry, readTaskTail(entry.OutputFile, 32*1024), true))
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
			return json.Marshal(formatBgEntry(entry, readTaskTail(entry.OutputFile, 32*1024), true))
		case <-ticker.C:
			entry = t.rt.Get(a.TaskID)
			if entry == nil {
				return json.Marshal(fmt.Sprintf("Task not found: %s", a.TaskID))
			}
			if entry.Status != task.Running {
				return json.Marshal(formatBgEntry(entry, readTaskTail(entry.OutputFile, 32*1024), true))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TaskStopTool — stop a background task
// ---------------------------------------------------------------------------

// TaskStopTool lets the model terminate a background task.
type TaskStopTool struct{ rt *task.Runtime }

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
// Constructors
// ---------------------------------------------------------------------------

// NewTaskTools creates the task tools for both planning and background execution.
// Pass nil for rt if background task tools are not needed.
func NewTaskTools(store *storage.TaskStore, rt *task.Runtime, hookRunner *hooks.Runner) []agentcore.Tool {
	tools := []agentcore.Tool{
		&TaskCreateTool{store: store, hooks: hookRunner},
		&TaskGetTool{store: store},
		&TaskUpdateTool{store: store, hooks: hookRunner},
		&TaskListTool{store: store},
	}
	if rt != nil {
		tools = append(tools,
			&TaskOutputTool{rt: rt},
			&TaskStopTool{rt: rt},
		)
	}
	return tools
}

func taskHookSnapshot(task *storage.Task) hooks.TaskSnapshot {
	if task == nil {
		return hooks.TaskSnapshot{}
	}
	return hooks.TaskSnapshot{
		ID:          task.ID,
		Subject:     task.Subject,
		Description: task.Description,
		ActiveForm:  task.ActiveForm,
		Status:      string(task.Status),
		Owner:       task.Owner,
		Blocks:      copyStrings(task.Blocks),
		BlockedBy:   copyStrings(task.BlockedBy),
		Metadata:    copyMetadata(task.Metadata),
	}
}

func previewTaskUpdate(task *storage.Task, opts storage.TaskUpdateOpts) *storage.Task {
	if task == nil {
		return nil
	}
	next := copyTask(task)
	if opts.Status != nil {
		next.Status = *opts.Status
	}
	if opts.Subject != nil {
		next.Subject = *opts.Subject
	}
	if opts.Description != nil {
		next.Description = *opts.Description
	}
	if opts.ActiveForm != nil {
		next.ActiveForm = *opts.ActiveForm
	}
	if opts.Owner != nil {
		next.Owner = *opts.Owner
	}
	if len(opts.Metadata) > 0 {
		if next.Metadata == nil {
			next.Metadata = make(map[string]any)
		}
		for k, v := range opts.Metadata {
			if v == nil {
				delete(next.Metadata, k)
			} else {
				next.Metadata[k] = v
			}
		}
	}
	if len(opts.AddBlocks) > 0 {
		next.Blocks = append(next.Blocks, opts.AddBlocks...)
	}
	if len(opts.AddBlockedBy) > 0 {
		next.BlockedBy = append(next.BlockedBy, opts.AddBlockedBy...)
	}
	return next
}

func copyStrings(src []string) []string {
	if src == nil {
		return nil
	}
	cp := make([]string, len(src))
	copy(cp, src)
	return cp
}

func copyMetadata(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}

func copyTask(t *storage.Task) *storage.Task {
	cp := *t
	cp.Blocks = copyStrings(t.Blocks)
	cp.BlockedBy = copyStrings(t.BlockedBy)
	cp.Metadata = copyMetadata(t.Metadata)
	return &cp
}

// ---------------------------------------------------------------------------
// Background task helpers
// ---------------------------------------------------------------------------

func formatBgEntry(entry *task.Entry, output string, withVerification bool) map[string]any {
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
	if entry.Type == task.TypeShell {
		result["command"] = entry.Command
		result["pid"] = entry.PID
		if entry.Status != task.Running {
			result["exit_code"] = entry.ExitCode
		}
	}
	if entry.Type == task.TypeSubAgent {
		result["agent"] = entry.Agent
		result["tool_count"] = entry.ToolCount
		result["tokens_in"] = entry.TokensIn
		result["tokens_out"] = entry.TokensOut
	}
	if withVerification && entry.Status != task.Running {
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
