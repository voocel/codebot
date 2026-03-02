package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

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
	Blocks      []string       `json:"blocks,omitempty"`
	BlockedBy   []string       `json:"blockedBy,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskSnapshot is a read-only snapshot of all tasks sent to the TUI.
type TaskSnapshot struct {
	Tasks      []Task
	Pending    int
	InProgress int
	Completed  int
	Total      int
}

// TaskNotifyFn is called after each store mutation with the latest snapshot.
type TaskNotifyFn func(TaskSnapshot)

// TaskStore is an in-memory, thread-safe task store with auto-increment IDs.
type TaskStore struct {
	mu       sync.RWMutex
	tasks    map[string]*Task
	nextID   int
	notifyFn TaskNotifyFn
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
		Metadata:    metadata,
	}
	s.tasks[id] = t
	snap := s.snapshot()
	s.mu.Unlock()
	s.notify(snap)
	return copyTask(t)
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

// UpdateOpts describes optional fields to update.
type UpdateOpts struct {
	Status      *TaskStatus
	Subject     *string
	Description *string
	ActiveForm  *string
	Owner       *string
	Metadata    map[string]any // merged; nil-valued keys are deleted
	AddBlocks   []string
	AddBlockedBy []string
}

// Update modifies a task and returns the updated copy.
func (s *TaskStore) Update(id string, opts UpdateOpts) (*Task, error) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("task %s not found", id)
	}

	if opts.Status != nil {
		if *opts.Status == "deleted" {
			delete(s.tasks, id)
			snap := s.snapshot()
			s.mu.Unlock()
			s.notify(snap)
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
	// Dependency tracking: bidirectional, matching Claude Code's addDependency().
	// addBlocks: this task blocks others → add id to each target's blockedBy.
	if len(opts.AddBlocks) > 0 {
		t.Blocks = appendUnique(t.Blocks, opts.AddBlocks...)
		for _, blockedID := range opts.AddBlocks {
			if other, exists := s.tasks[blockedID]; exists {
				other.BlockedBy = appendUnique(other.BlockedBy, id)
			}
		}
	}
	// addBlockedBy: others block this task → add id to each blocker's blocks.
	if len(opts.AddBlockedBy) > 0 {
		t.BlockedBy = appendUnique(t.BlockedBy, opts.AddBlockedBy...)
		for _, blockerID := range opts.AddBlockedBy {
			if other, exists := s.tasks[blockerID]; exists {
				other.Blocks = appendUnique(other.Blocks, id)
			}
		}
	}

	cp := copyTask(t)
	snap := s.snapshot()
	s.mu.Unlock()
	s.notify(snap)
	return cp, nil
}

// List returns copies of all non-deleted tasks sorted by ID.
func (s *TaskStore) List() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, *copyTask(t))
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, _ := strconv.Atoi(tasks[i].ID)
		b, _ := strconv.Atoi(tasks[j].ID)
		return a < b
	})
	return tasks
}

// snapshot builds a TaskSnapshot (must be called with mu held).
func (s *TaskStore) snapshot() TaskSnapshot {
	snap := TaskSnapshot{}
	tasks := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, *copyTask(t))
		switch t.Status {
		case TaskPending:
			snap.Pending++
		case TaskInProgress:
			snap.InProgress++
		case TaskCompleted:
			snap.Completed++
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, _ := strconv.Atoi(tasks[i].ID)
		b, _ := strconv.Atoi(tasks[j].ID)
		return a < b
	})
	snap.Tasks = tasks
	snap.Total = len(tasks)
	return snap
}

func (s *TaskStore) notify(snap TaskSnapshot) {
	s.mu.RLock()
	fn := s.notifyFn
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

func copyStrings(s []string) []string {
	if s == nil {
		return nil
	}
	cp := make([]string, len(s))
	copy(cp, s)
	return cp
}

func appendUnique(base []string, vals ...string) []string {
	set := make(map[string]struct{}, len(base))
	for _, v := range base {
		set[v] = struct{}{}
	}
	for _, v := range vals {
		if _, ok := set[v]; !ok {
			base = append(base, v)
			set[v] = struct{}{}
		}
	}
	return base
}

// ---------------------------------------------------------------------------
// TaskCreateTool
// ---------------------------------------------------------------------------

// TaskCreateTool creates new tasks.
type TaskCreateTool struct{ store *TaskStore }

func (t *TaskCreateTool) Name() string  { return "task_create" }
func (t *TaskCreateTool) Label() string { return "Create Task" }
func (t *TaskCreateTool) Description() string {
	return `Create a structured task to track progress on multi-step work. Tasks support dependencies (blocks/blockedBy) for coordinating parallel work streams.`
}
func (t *TaskCreateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("subject", schema.String("Brief imperative title (e.g. 'Fix authentication bug')")).Required(),
		schema.Property("description", schema.String("Detailed description of what needs to be done")).Required(),
		schema.Property("activeForm", schema.String("Present continuous form for progress display (e.g. 'Fixing authentication bug')")),
	)
}

// SetNotifyFn registers the TUI notification callback (delegates to store).
func (t *TaskCreateTool) SetNotifyFn(fn TaskNotifyFn) {
	t.store.SetNotifyFn(fn)
}

type taskCreateArgs struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"activeForm"`
}

func (t *TaskCreateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a taskCreateArgs
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
	return `Retrieve full details of a task by its ID, including description, status, and dependencies.`
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
type TaskUpdateTool struct{ store *TaskStore }

func (t *TaskUpdateTool) Name() string  { return "task_update" }
func (t *TaskUpdateTool) Label() string { return "Update Task" }
func (t *TaskUpdateTool) Description() string {
	return `Update a task's status, fields, or dependencies. Set status to "deleted" to remove a task. Use addBlocks/addBlockedBy to set up dependency chains between tasks.`
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

type taskUpdateArgs struct {
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

func (t *TaskUpdateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a taskUpdateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.TaskID == "" {
		return json.Marshal("Validation error: taskId is required")
	}

	updated, err := t.store.Update(a.TaskID, UpdateOpts{
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
	return json.Marshal(fmt.Sprintf("Task #%s updated successfully", a.TaskID))
}

// ---------------------------------------------------------------------------
// TaskListTool
// ---------------------------------------------------------------------------

// TaskListTool lists all tasks.
type TaskListTool struct{ store *TaskStore }

func (t *TaskListTool) Name() string  { return "task_list" }
func (t *TaskListTool) Label() string { return "List Tasks" }
func (t *TaskListTool) Description() string {
	return `List all tasks with their status, subject, and dependencies. Use to check overall progress and find available tasks.`
}
func (t *TaskListTool) Schema() map[string]any {
	return schema.Object()
}

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
			line += fmt.Sprintf(" [blocked by: %s]", strings.Join(task.BlockedBy, ", "))
		}
		sb.WriteString(line + "\n")
	}
	return json.Marshal(strings.TrimRight(sb.String(), "\n"))
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewTaskTools creates a TaskStore and the four task tools that share it.
func NewTaskTools() (*TaskStore, []agentcore.Tool) {
	store := NewTaskStore()
	return store, []agentcore.Tool{
		&TaskCreateTool{store: store},
		&TaskGetTool{store: store},
		&TaskUpdateTool{store: store},
		&TaskListTool{store: store},
	}
}
