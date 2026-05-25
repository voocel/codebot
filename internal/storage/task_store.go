package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sentinel errors returned by Claim so callers can branch on cause without
// string-matching.
var (
	ErrTaskNotFound    = errors.New("task not found")
	ErrAlreadyClaimed  = errors.New("task already claimed by another owner")
	ErrAlreadyResolved = errors.New("task already completed")
	ErrBlocked         = errors.New("task has unresolved blockers")
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
	// CompletedAt is set when the task transitions into TaskCompleted and
	// cleared if it ever reverts to a non-completed state. Pointer + omitempty
	// keeps older persisted JSON files (without this field) loading cleanly.
	// The TUI uses it to keep recently-completed tasks pinned to the top of
	// the truncated task tree for ~30s before they sink to the bottom.
	CompletedAt *time.Time `json:"completedAt,omitempty"`
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
	s.removeFileLocked(id)
	s.mu.Unlock()
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
	hwm := s.nextID - 1
	s.persistLocked(cp)
	s.writeHighWaterMarkLocked(hwm)
	s.mu.Unlock()
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
			s.removeFileLocked(id)
			s.mu.Unlock()
			s.notify()
			return nil, nil
		}
		prev := t.Status
		t.Status = *opts.Status
		switch {
		case t.Status == TaskCompleted && prev != TaskCompleted:
			now := time.Now()
			t.CompletedAt = &now
		case t.Status != TaskCompleted && prev == TaskCompleted:
			t.CompletedAt = nil
		}
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
	s.persistLocked(cp)
	for _, tc := range touchedCopies {
		s.persistLocked(tc)
	}
	s.mu.Unlock()
	s.notify()
	return cp, nil
}

// Claim atomically assigns owner to task id, but only when the task is in a
// claimable state: it exists, has no current owner (or already equals
// owner — idempotent retry), is not completed, and all of its blockedBy
// dependencies are completed. Returns the updated task on success or a
// sentinel error explaining the rejection.
//
// This is the CAS primitive that makes work-stealing safe: when two idle
// teammates race for the same unowned task, only the first Claim succeeds;
// the loser sees ErrAlreadyClaimed and falls through to find another task.
// The in-memory mutex is the only lock — codebot is single-process.
//
// Deliberately NOT implemented: a "busy-check" variant that rejects when
// the claimant already owns another open task. The pull path is strictly
// sequential per agent (FindClaimable → Claim → run turn → return → pull
// again), so one-in-flight-per-agent holds by construction; the only case
// such a check would catch is a model marking two tasks in_progress
// inside the same turn — a degenerate prompt-shape problem, not a race.
func (s *TaskStore) Claim(id, owner string) (*Task, error) {
	if owner == "" {
		return nil, fmt.Errorf("Claim: owner must be non-empty")
	}
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrTaskNotFound
	}
	if t.Owner != "" && t.Owner != owner {
		s.mu.Unlock()
		return nil, ErrAlreadyClaimed
	}
	if t.Status == TaskCompleted {
		s.mu.Unlock()
		return nil, ErrAlreadyResolved
	}
	for _, blocker := range t.BlockedBy {
		other, exists := s.tasks[blocker]
		if !exists {
			continue
		}
		if other.Status != TaskCompleted {
			s.mu.Unlock()
			return nil, ErrBlocked
		}
	}
	// Idempotent path: if owner already matches we still return a fresh copy
	// (callers treat the return value as authoritative) but skip persistence
	// — the on-disk file is already in the desired state and rewriting it
	// would burn IO without changing anything observable.
	persistNeeded := t.Owner != owner
	t.Owner = owner
	cp := copyTask(t)
	if persistNeeded {
		s.persistLocked(cp)
	}
	s.mu.Unlock()
	if persistNeeded {
		s.notify()
	}
	return cp, nil
}

// FindClaimable returns a copy of the next claimable task by ascending ID, or
// nil when nothing is available. Claimable = owner empty, status != completed,
// and every blockedBy reference points to a completed task. Pure read; safe
// for the dispatcher hot loop.
func (s *TaskStore) FindClaimable() *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := sortedTasks(s.tasks)
	statusByID := make(map[string]TaskStatus, len(items))
	for _, item := range items {
		statusByID[item.ID] = item.Status
	}
	for i := range items {
		if items[i].Owner != "" {
			continue
		}
		if items[i].Status == TaskCompleted {
			continue
		}
		blocked := false
		for _, blocker := range items[i].BlockedBy {
			if statusByID[blocker] != TaskCompleted {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		cp := items[i]
		return &cp
	}
	return nil
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
	SortTasksByID(out)
	return out
}

// SortTasksByID sorts tasks by numeric ID ascending.
func SortTasksByID(tasks []Task) {
	sort.Slice(tasks, func(i, j int) bool {
		return CompareTaskIDs(tasks[i].ID, tasks[j].ID) < 0
	})
}

// CompareTaskIDs compares two task ID strings numerically.
func CompareTaskIDs(a, b string) int {
	ai, aErr := strconv.Atoi(a)
	bi, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return ai - bi
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
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

// SetDir enables file persistence. The directory is created lazily on the
// first write (persistLocked / writeHighWaterMarkLocked) — sessions that
// never create a task leave nothing behind on disk.
func (s *TaskStore) SetDir(dir string) error {
	s.mu.Lock()
	s.dir = dir
	s.mu.Unlock()
	return s.loadFromDir()
}

func (s *TaskStore) loadFromDir() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // dir not created yet — no prior tasks to load
		}
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

func (s *TaskStore) persistLocked(t *Task) {
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
	if err := taskWriteFileAtomic(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write task %s: %v\n", t.ID, err)
	}
}

func (s *TaskStore) removeFileLocked(id string) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeHighWaterMarkLocked(id)
}

func (s *TaskStore) writeHighWaterMarkLocked(id int) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recreate task dir %s: %v\n", s.dir, err)
		return
	}
	if err := taskWriteFileAtomic(filepath.Join(s.dir, taskHighWaterMarkFile), []byte(strconv.Itoa(id)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write task high water mark: %v\n", err)
	}
}

func taskWriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".task-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
