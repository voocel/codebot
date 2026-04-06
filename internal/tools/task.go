package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/hooks"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
)

// Task is a single tracked work item.
type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description,omitempty"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Status      TaskStatus     `json:"status"`
	Owner       string         `json:"owner,omitempty"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskSnapshot is a read-only snapshot of all tasks sent to the TUI.
type TaskSnapshot struct {
	Items      []Task
	Pending    int
	InProgress int
	Completed  int
	Total      int
}

// TaskNotifyFn is called after each store mutation with the latest snapshot.
type TaskNotifyFn func(TaskSnapshot)

// ---------------------------------------------------------------------------
// TaskStore
// ---------------------------------------------------------------------------

// TaskStore is a thread-safe task store with optional file persistence.
// Currently session-scoped (dir = tasks/{sessionID}). TODO(team): support
// shared task list IDs so multiple sessions/agents can collaborate on the
// same task list, with file locking and cross-process change notification.
type TaskStore struct {
	mu       sync.RWMutex
	tasks    map[string]*Task
	nextID   int
	notifyFn TaskNotifyFn
	dir      string // persistence directory; empty = in-memory only
}

// NewTaskStore creates an empty store.
func NewTaskStore() *TaskStore {
	return &TaskStore{
		tasks:  make(map[string]*Task),
		nextID: 1,
	}
}

// SetNotifyFn registers a callback invoked after every mutation.
func (s *TaskStore) SetNotifyFn(fn TaskNotifyFn) {
	s.mu.Lock()
	s.notifyFn = fn
	s.mu.Unlock()
}

// Reset clears all tasks from memory and persistence while preserving the
// monotonic task ID sequence for future task creation.
func (s *TaskStore) Reset() error {
	s.mu.RLock()
	currentHighWaterMark := s.nextID - 1
	for _, task := range s.tasks {
		if id, err := strconv.Atoi(task.ID); err == nil && id > currentHighWaterMark {
			currentHighWaterMark = id
		}
	}
	dir := s.dir
	nextID := s.nextID
	s.mu.RUnlock()

	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read task dir: %w", err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove task file %s: %w", name, err)
			}
		}
		s.writeHighWaterMark(currentHighWaterMark)
	}

	s.mu.Lock()
	s.tasks = make(map[string]*Task)
	if nextID < currentHighWaterMark+1 {
		s.nextID = currentHighWaterMark + 1
	}
	s.mu.Unlock()

	s.notify()
	return nil
}

// Delete removes a task and any persisted file.
func (s *TaskStore) Delete(id string) bool {
	s.mu.Lock()
	if _, ok := s.tasks[id]; !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.tasks, id)
	s.mu.Unlock()
	s.removeFile(id)
	s.notify()
	return true
}

// Create adds a new task and returns a copy.
func (s *TaskStore) Create(subject, description, activeForm string, metadata map[string]any) *Task {
	s.mu.Lock()
	id := strconv.Itoa(s.nextID)
	s.nextID++
	t := &Task{
		ID:          id,
		Subject:     subject,
		Description: description,
		ActiveForm:  activeForm,
		Status:      TaskPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
		Metadata:    metadata,
	}
	s.tasks[id] = t
	cp := copyTask(t)
	dir := s.dir
	hwm := s.nextID - 1
	s.mu.Unlock()
	if dir != "" {
		s.persist(cp)
		s.writeHighWaterMark(hwm)
	}
	s.notify()
	return cp
}

// Get returns a copy of the task or false if not found.
func (s *TaskStore) Get(id string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	return copyTask(t), true
}

// TaskUpdateOpts describes optional fields to update.
type TaskUpdateOpts struct {
	Status       *TaskStatus
	Subject      *string
	Description  *string
	ActiveForm   *string
	Owner        *string
	Metadata     map[string]any // merged; nil-valued keys are deleted
	AddBlocks    []string
	AddBlockedBy []string
}

// Update modifies a task and returns the updated copy.
func (s *TaskStore) Update(id string, opts TaskUpdateOpts) (*Task, error) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("task %s not found", id)
	}

	if opts.Status != nil {
		if *opts.Status == "deleted" {
			delete(s.tasks, id)
			s.mu.Unlock()
			s.removeFile(id)
			s.notify()
			return nil, nil
		}
		t.Status = *opts.Status
	}
	if opts.Subject != nil {
		t.Subject = *opts.Subject
	}
	if opts.Description != nil {
		t.Description = *opts.Description
	}
	if opts.ActiveForm != nil {
		t.ActiveForm = *opts.ActiveForm
	}
	if opts.Owner != nil {
		t.Owner = *opts.Owner
	}
	if len(opts.Metadata) > 0 {
		if t.Metadata == nil {
			t.Metadata = make(map[string]any)
		}
		for k, v := range opts.Metadata {
			if v == nil {
				delete(t.Metadata, k)
			} else {
				t.Metadata[k] = v
			}
		}
	}
	// Dependency tracking: bidirectional.
	var touched []*Task
	if len(opts.AddBlocks) > 0 {
		t.Blocks = taskAppendUnique(t.Blocks, opts.AddBlocks...)
		for _, blockedID := range opts.AddBlocks {
			if other, exists := s.tasks[blockedID]; exists {
				other.BlockedBy = taskAppendUnique(other.BlockedBy, id)
				touched = append(touched, other)
			}
		}
	}
	if len(opts.AddBlockedBy) > 0 {
		t.BlockedBy = taskAppendUnique(t.BlockedBy, opts.AddBlockedBy...)
		for _, blockerID := range opts.AddBlockedBy {
			if other, exists := s.tasks[blockerID]; exists {
				other.Blocks = taskAppendUnique(other.Blocks, id)
				touched = append(touched, other)
			}
		}
	}

	cp := copyTask(t)
	var touchedCopies []*Task
	for _, other := range touched {
		touchedCopies = append(touchedCopies, copyTask(other))
	}
	s.mu.Unlock()

	s.persist(cp)
	for _, tc := range touchedCopies {
		s.persist(tc)
	}
	s.notify()
	return cp, nil
}

// List returns copies of all tasks sorted by ID.
func (s *TaskStore) List() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTasks(s.tasks)
}

// Snapshot returns the current read-only snapshot.
func (s *TaskStore) Snapshot() TaskSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot()
}

func (s *TaskStore) snapshot() TaskSnapshot {
	snap := TaskSnapshot{}
	items := sortedTasks(s.tasks)
	for _, t := range items {
		switch t.Status {
		case TaskPending:
			snap.Pending++
		case TaskInProgress:
			snap.InProgress++
		case TaskCompleted:
			snap.Completed++
		}
	}
	snap.Items = items
	snap.Total = len(items)
	return snap
}

func (s *TaskStore) notify() {
	s.mu.RLock()
	fn := s.notifyFn
	snap := s.snapshot()
	s.mu.RUnlock()
	if fn != nil {
		fn(snap)
	}
}

func copyTask(t *Task) *Task {
	cp := *t
	cp.Blocks = copyStrings(t.Blocks)
	cp.BlockedBy = copyStrings(t.BlockedBy)
	if t.Metadata != nil {
		cp.Metadata = make(map[string]any, len(t.Metadata))
		maps.Copy(cp.Metadata, t.Metadata)
	}
	return &cp
}

func sortedTasks(tasks map[string]*Task) []Task {
	out := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, *copyTask(t))
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].ID)
		b, _ := strconv.Atoi(out[j].ID)
		return a < b
	})
	return out
}

func copyStrings(src []string) []string {
	if src == nil {
		return nil
	}
	cp := make([]string, len(src))
	copy(cp, src)
	return cp
}

func taskAppendUnique(base []string, vals ...string) []string {
	set := make(map[string]struct{}, len(base))
	for _, v := range base {
		set[v] = struct{}{}
	}
	for _, v := range vals {
		if _, ok := set[v]; ok {
			continue
		}
		base = append(base, v)
		set[v] = struct{}{}
	}
	return base
}

// ---------------------------------------------------------------------------
// Persistence helpers
// ---------------------------------------------------------------------------

const taskHighWaterMarkFile = ".highwatermark"

// SetDir enables file persistence. It creates the directory if needed and
// loads any existing tasks from disk. Call before the store is used.
func (s *TaskStore) SetDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create task dir: %w", err)
	}
	s.mu.Lock()
	s.dir = dir
	s.mu.Unlock()
	return s.loadFromDir()
}

func (s *TaskStore) loadFromDir() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read task dir: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	hwm := s.readHighWaterMark()
	maxID := hwm
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		idStr := strings.TrimSuffix(name, ".json")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: read task %s: %v\n", name, err)
			continue
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			fmt.Fprintf(os.Stderr, "warning: parse task %s: %v\n", name, err)
			continue
		}
		s.tasks[t.ID] = &t
		if id > maxID {
			maxID = id
		}
	}
	s.nextID = maxID + 1
	return nil
}

func (s *TaskStore) persist(t *Task) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recreate task dir %s: %v\n", s.dir, err)
		return
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: marshal task %s: %v\n", t.ID, err)
		return
	}
	path := filepath.Join(s.dir, t.ID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write task %s: %v\n", t.ID, err)
	}
}

func (s *TaskStore) removeFile(id string) {
	if s.dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(s.dir, id+".json"))
}

func (s *TaskStore) readHighWaterMark() int {
	data, err := os.ReadFile(filepath.Join(s.dir, taskHighWaterMarkFile))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

func (s *TaskStore) writeHighWaterMark(id int) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recreate task dir %s: %v\n", s.dir, err)
		return
	}
	_ = os.WriteFile(filepath.Join(s.dir, taskHighWaterMarkFile), []byte(strconv.Itoa(id)), 0o644)
}

// ---------------------------------------------------------------------------
// TaskCreateTool
// ---------------------------------------------------------------------------

// TaskCreateTool creates new tasks.
type TaskCreateTool struct {
	store *TaskStore
	hooks *hooks.Runner
}

func (t *TaskCreateTool) Name() string  { return "task_create" }
func (t *TaskCreateTool) Label() string { return "Create Task" }
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
func (t *TaskCreateTool) SetNotifyFn(fn TaskNotifyFn) { t.store.SetNotifyFn(fn) }

// Store returns the underlying TaskStore.
func (t *TaskCreateTool) Store() *TaskStore { return t.store }

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
	return json.Marshal(fmt.Sprintf("Task #%s created successfully: %s", task.ID, task.Subject))
}

// ---------------------------------------------------------------------------
// TaskGetTool
// ---------------------------------------------------------------------------

// TaskGetTool retrieves a single task by ID.
type TaskGetTool struct{ store *TaskStore }

func (t *TaskGetTool) Name() string  { return "task_get" }
func (t *TaskGetTool) Label() string { return "Get Task" }
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
	store *TaskStore
	hooks *hooks.Runner
}

func (t *TaskUpdateTool) Name() string  { return "task_update" }
func (t *TaskUpdateTool) Label() string { return "Update Task" }
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
		TaskID       string         `json:"taskId"`
		Status       *TaskStatus    `json:"status,omitempty"`
		Subject      *string        `json:"subject,omitempty"`
		Description  *string        `json:"description,omitempty"`
		ActiveForm   *string        `json:"activeForm,omitempty"`
		Owner        *string        `json:"owner,omitempty"`
		Metadata     map[string]any `json:"metadata,omitempty"`
		AddBlocks    []string       `json:"addBlocks,omitempty"`
		AddBlockedBy []string       `json:"addBlockedBy,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.TaskID == "" {
		return json.Marshal("Validation error: taskId is required")
	}
	if t.hooks != nil && a.Status != nil && *a.Status == TaskCompleted {
		existing, ok := t.store.Get(a.TaskID)
		if !ok {
			return json.Marshal(fmt.Sprintf("Error: task %s not found", a.TaskID))
		}
		if existing.Status != TaskCompleted {
			next := previewTaskUpdate(existing, TaskUpdateOpts{
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
	updated, err := t.store.Update(a.TaskID, TaskUpdateOpts{
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
	result := map[string]any{
		"id":      updated.ID,
		"status":  updated.Status,
		"message": fmt.Sprintf("Task #%s updated successfully", updated.ID),
	}
	if updated.Status == TaskCompleted {
		result["verification_needed"] = true
		result["hint"] = "Verify the result before proceeding to the next task."
	}
	return json.Marshal(result)
}

// ---------------------------------------------------------------------------
// TaskListTool
// ---------------------------------------------------------------------------

// TaskListTool lists all tasks.
type TaskListTool struct{ store *TaskStore }

func (t *TaskListTool) Name() string  { return "task_list" }
func (t *TaskListTool) Label() string { return "List Tasks" }
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
	tasks := t.store.List()
	if len(tasks) == 0 {
		return json.Marshal("No tasks")
	}

	var sb strings.Builder
	var pending, inProgress, completed int
	for _, task := range tasks {
		switch task.Status {
		case TaskPending:
			pending++
		case TaskInProgress:
			inProgress++
		case TaskCompleted:
			completed++
		}
	}
	fmt.Fprintf(&sb, "Tasks: %d total (%d pending, %d in_progress, %d completed)\n",
		len(tasks), pending, inProgress, completed)

	for _, task := range tasks {
		line := fmt.Sprintf("- #%s [%s] %s", task.ID, task.Status, task.Subject)
		if task.Owner != "" {
			line += fmt.Sprintf(" (owner: %s)", task.Owner)
		}
		if len(task.BlockedBy) > 0 {
			// Filter out completed blockers.
			var active []string
			for _, bid := range task.BlockedBy {
				if t, ok := t.store.Get(bid); ok && t.Status != TaskCompleted {
					active = append(active, bid)
				}
			}
			if len(active) > 0 {
				line += fmt.Sprintf(" [blocked by: %s]", strings.Join(active, ", "))
			}
		}
		sb.WriteString(line + "\n")
	}
	return json.Marshal(strings.TrimRight(sb.String(), "\n"))
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
			if entry.Status != agentcore.TaskRunning {
				return json.Marshal(formatBgEntry(entry, readTaskTail(entry.OutputFile, 32*1024), true))
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
// Constructors
// ---------------------------------------------------------------------------

// NewTaskTools creates the task tools for both planning and background execution.
// Pass nil for rt if background task tools are not needed.
func NewTaskTools(store *TaskStore, rt *agentcore.TaskRuntime, hookRunner *hooks.Runner) []agentcore.Tool {
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

func taskHookSnapshot(task *Task) hooks.TaskSnapshot {
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

func previewTaskUpdate(task *Task, opts TaskUpdateOpts) *Task {
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
		next.Blocks = taskAppendUnique(next.Blocks, opts.AddBlocks...)
	}
	if len(opts.AddBlockedBy) > 0 {
		next.BlockedBy = taskAppendUnique(next.BlockedBy, opts.AddBlockedBy...)
	}
	return next
}

func copyMetadata(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}

// ---------------------------------------------------------------------------
// Background task helpers
// ---------------------------------------------------------------------------

func formatBgEntry(entry *agentcore.BackgroundTaskEntry, output string, withVerification bool) map[string]any {
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
