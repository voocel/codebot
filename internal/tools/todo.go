package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// TodoStatus represents the lifecycle state of a todo item.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoDone       TodoStatus = "done"
)

// TodoItem is a single tracked plan/work item.
type TodoItem struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Status      TodoStatus     `json:"status"`
	Owner       string         `json:"owner,omitempty"`
	DependsOn   []string       `json:"dependsOn"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TodoSnapshot is a read-only snapshot of all todo items sent to the TUI.
type TodoSnapshot struct {
	Items      []TodoItem
	Pending    int
	InProgress int
	Done       int
	Total      int
}

// TodoNotifyFn is called after each store mutation with the latest snapshot.
type TodoNotifyFn func(TodoSnapshot)

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// TodoStore is a thread-safe todo store with optional file persistence.
type TodoStore struct {
	mu       sync.RWMutex
	items    map[string]*TodoItem
	nextID   int
	notifyFn TodoNotifyFn
	dir      string // persistence directory; empty = in-memory only
}

// NewTodoStore creates an empty store.
func NewTodoStore() *TodoStore {
	return &TodoStore{
		items:  make(map[string]*TodoItem),
		nextID: 1,
	}
}

// SetNotifyFn registers a callback invoked after every mutation.
func (s *TodoStore) SetNotifyFn(fn TodoNotifyFn) {
	s.mu.Lock()
	s.notifyFn = fn
	s.mu.Unlock()
}

// Create adds a new todo item and returns a copy.
func (s *TodoStore) Create(title, description, activeForm string, metadata map[string]any) *TodoItem {
	s.mu.Lock()
	id := strconv.Itoa(s.nextID)
	s.nextID++
	item := &TodoItem{
		ID:          id,
		Title:       title,
		Description: description,
		ActiveForm:  activeForm,
		Status:      TodoPending,
		DependsOn:   []string{},
		Metadata:    metadata,
	}
	s.items[id] = item
	cp := copyTodoItem(item)
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

// Get returns a copy of the todo item or false if not found.
func (s *TodoStore) Get(id string) (*TodoItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	if !ok {
		return nil, false
	}
	return copyTodoItem(item), true
}

// TodoUpdateOpts describes optional fields to update.
type TodoUpdateOpts struct {
	Status       *TodoStatus
	Title        *string
	Description  *string
	ActiveForm   *string
	Owner        *string
	Metadata     map[string]any // merged; nil-valued keys are deleted
	AddDependsOn []string
}

// Update modifies a todo item and returns the updated copy.
func (s *TodoStore) Update(id string, opts TodoUpdateOpts) (*TodoItem, error) {
	s.mu.Lock()
	item, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("todo item %s not found", id)
	}

	if opts.Status != nil {
		if *opts.Status == "deleted" {
			delete(s.items, id)
			s.mu.Unlock()
			s.removeFile(id)
			s.notify()
			return nil, nil
		}
		item.Status = *opts.Status
	}
	if opts.Title != nil {
		item.Title = *opts.Title
	}
	if opts.Description != nil {
		item.Description = *opts.Description
	}
	if opts.ActiveForm != nil {
		item.ActiveForm = *opts.ActiveForm
	}
	if opts.Owner != nil {
		item.Owner = *opts.Owner
	}
	if len(opts.Metadata) > 0 {
		if item.Metadata == nil {
			item.Metadata = make(map[string]any)
		}
		for k, v := range opts.Metadata {
			if v == nil {
				delete(item.Metadata, k)
			} else {
				item.Metadata[k] = v
			}
		}
	}
	if len(opts.AddDependsOn) > 0 {
		item.DependsOn = todoAppendUnique(item.DependsOn, opts.AddDependsOn...)
	}

	cp := copyTodoItem(item)
	allDone := s.allDone()
	s.mu.Unlock()

	s.persist(cp)
	if allDone {
		s.clearAll()
	}
	s.notify()
	return cp, nil
}

// List returns copies of all todo items sorted by ID.
func (s *TodoStore) List() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTodoItems(s.items)
}

// Snapshot returns the current read-only snapshot.
func (s *TodoStore) Snapshot() TodoSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot()
}

// BlockedBy returns IDs of items that depend on the given ID.
func (s *TodoStore) BlockedBy(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var blocked []string
	for _, item := range s.items {
		if slices.Contains(item.DependsOn, id) {
			blocked = append(blocked, item.ID)
		}
	}
	sort.Slice(blocked, func(i, j int) bool {
		a, _ := strconv.Atoi(blocked[i])
		b, _ := strconv.Atoi(blocked[j])
		return a < b
	})
	return blocked
}

func (s *TodoStore) snapshot() TodoSnapshot {
	snap := TodoSnapshot{}
	items := sortedTodoItems(s.items)
	for _, item := range items {
		switch item.Status {
		case TodoPending:
			snap.Pending++
		case TodoInProgress:
			snap.InProgress++
		case TodoDone:
			snap.Done++
		}
	}
	snap.Items = items
	snap.Total = len(items)
	return snap
}

func (s *TodoStore) notify() {
	s.mu.RLock()
	fn := s.notifyFn
	snap := s.snapshot()
	s.mu.RUnlock()
	if fn != nil {
		fn(snap)
	}
}

func copyTodoItem(item *TodoItem) *TodoItem {
	cp := *item
	cp.DependsOn = copyTodoStrings(item.DependsOn)
	if item.Metadata != nil {
		cp.Metadata = make(map[string]any, len(item.Metadata))
		maps.Copy(cp.Metadata, item.Metadata)
	}
	return &cp
}

func sortedTodoItems(items map[string]*TodoItem) []TodoItem {
	out := make([]TodoItem, 0, len(items))
	for _, item := range items {
		out = append(out, *copyTodoItem(item))
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].ID)
		b, _ := strconv.Atoi(out[j].ID)
		return a < b
	})
	return out
}

func copyTodoStrings(src []string) []string {
	if src == nil {
		return nil
	}
	cp := make([]string, len(src))
	copy(cp, src)
	return cp
}

func todoAppendUnique(base []string, vals ...string) []string {
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

const todoHighWaterMarkFile = ".highwatermark"

// SetDir enables file persistence. It creates the directory if needed and
// loads any existing items from disk. Call before the store is used.
func (s *TodoStore) SetDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create todo dir: %w", err)
	}
	s.mu.Lock()
	s.dir = dir
	s.mu.Unlock()
	return s.loadFromDir()
}

func (s *TodoStore) loadFromDir() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read todo dir: %w", err)
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
			fmt.Fprintf(os.Stderr, "warning: read todo item %s: %v\n", name, err)
			continue
		}
		var item TodoItem
		if err := json.Unmarshal(data, &item); err != nil {
			fmt.Fprintf(os.Stderr, "warning: parse todo item %s: %v\n", name, err)
			continue
		}
		s.items[item.ID] = &item
		if id > maxID {
			maxID = id
		}
	}
	s.nextID = maxID + 1
	return nil
}

func (s *TodoStore) persist(item *TodoItem) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recreate todo dir %s: %v\n", s.dir, err)
		return
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: marshal todo item %s: %v\n", item.ID, err)
		return
	}
	path := filepath.Join(s.dir, item.ID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write todo item %s: %v\n", item.ID, err)
	}
}

func (s *TodoStore) removeFile(id string) {
	if s.dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(s.dir, id+".json"))
}

func (s *TodoStore) readHighWaterMark() int {
	data, err := os.ReadFile(filepath.Join(s.dir, todoHighWaterMarkFile))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

func (s *TodoStore) writeHighWaterMark(id int) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recreate todo dir %s: %v\n", s.dir, err)
		return
	}
	_ = os.WriteFile(filepath.Join(s.dir, todoHighWaterMarkFile), []byte(strconv.Itoa(id)), 0o644)
}

func (s *TodoStore) allDone() bool {
	if len(s.items) == 0 {
		return false
	}
	for _, item := range s.items {
		if item.Status != TodoDone {
			return false
		}
	}
	return true
}

func (s *TodoStore) clearAll() {
	s.mu.Lock()
	clear(s.items)
	dir := s.dir
	s.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// ---------------------------------------------------------------------------
// TodoCreateTool
// ---------------------------------------------------------------------------

// TodoCreateTool creates new todo items.
type TodoCreateTool struct{ store *TodoStore }

func (t *TodoCreateTool) Name() string  { return "todo_create" }
func (t *TodoCreateTool) Label() string { return "Create Todo Item" }
func (t *TodoCreateTool) Description() string {
	return "Create a structured todo item to track multi-step work."
}
func (t *TodoCreateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("title", schema.String("Brief imperative title")).Required(),
		schema.Property("description", schema.String("Detailed description")).Required(),
		schema.Property("activeForm", schema.String("Present continuous progress label")),
	)
}

// SetNotifyFn registers the TUI notification callback (delegates to store).
func (t *TodoCreateTool) SetNotifyFn(fn TodoNotifyFn) { t.store.SetNotifyFn(fn) }

// Store returns the underlying TodoStore (used for persistence wiring).
func (t *TodoCreateTool) Store() *TodoStore { return t.store }

func (t *TodoCreateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		ActiveForm  string `json:"activeForm"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Title == "" {
		return json.Marshal("Validation error: title is required")
	}
	if a.Description == "" {
		return json.Marshal("Validation error: description is required")
	}
	item := t.store.Create(a.Title, a.Description, a.ActiveForm, nil)
	return json.Marshal(map[string]any{
		"id":      item.ID,
		"title":   item.Title,
		"message": fmt.Sprintf("Todo item #%s created", item.ID),
	})
}

// ---------------------------------------------------------------------------
// TodoGetTool
// ---------------------------------------------------------------------------

// TodoGetTool retrieves a single todo item by ID.
type TodoGetTool struct{ store *TodoStore }

func (t *TodoGetTool) Name() string  { return "todo_get" }
func (t *TodoGetTool) Label() string { return "Get Todo Item" }
func (t *TodoGetTool) Description() string {
	return "Retrieve a single todo item by its ID."
}
func (t *TodoGetTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("Todo item ID")).Required(),
	)
}

func (t *TodoGetTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	item, ok := t.store.Get(a.ID)
	if !ok {
		return json.Marshal(fmt.Sprintf("Todo item %s not found", a.ID))
	}
	return json.Marshal(item)
}

// ---------------------------------------------------------------------------
// TodoUpdateTool
// ---------------------------------------------------------------------------

// TodoUpdateTool modifies an existing todo item.
type TodoUpdateTool struct{ store *TodoStore }

func (t *TodoUpdateTool) Name() string  { return "todo_update" }
func (t *TodoUpdateTool) Label() string { return "Update Todo Item" }
func (t *TodoUpdateTool) Description() string {
	return "Update a todo item's status, fields, or dependencies."
}
func (t *TodoUpdateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("Todo item ID")).Required(),
		schema.Property("status", schema.Enum("New status", "pending", "in_progress", "done", "deleted")),
		schema.Property("title", schema.String("New title")),
		schema.Property("description", schema.String("New description")),
		schema.Property("activeForm", schema.String("New progress label")),
		schema.Property("owner", schema.String("Owner")),
		schema.Property("metadata", schema.Object()),
		schema.Property("addDependsOn", schema.Array("Prerequisite todo item IDs", schema.String("todo item ID"))),
	)
}

func (t *TodoUpdateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		ID           string         `json:"id"`
		Status       *TodoStatus    `json:"status,omitempty"`
		Title        *string        `json:"title,omitempty"`
		Description  *string        `json:"description,omitempty"`
		ActiveForm   *string        `json:"activeForm,omitempty"`
		Owner        *string        `json:"owner,omitempty"`
		Metadata     map[string]any `json:"metadata,omitempty"`
		AddDependsOn []string       `json:"addDependsOn,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		return json.Marshal("Validation error: id is required")
	}
	updated, err := t.store.Update(a.ID, TodoUpdateOpts{
		Status:       a.Status,
		Title:        a.Title,
		Description:  a.Description,
		ActiveForm:   a.ActiveForm,
		Owner:        a.Owner,
		Metadata:     a.Metadata,
		AddDependsOn: a.AddDependsOn,
	})
	if err != nil {
		return json.Marshal(fmt.Sprintf("Error: %s", err))
	}
	if updated == nil {
		return json.Marshal(fmt.Sprintf("Todo item %s deleted", a.ID))
	}
	result := map[string]any{
		"id":      updated.ID,
		"status":  updated.Status,
		"message": fmt.Sprintf("Todo item #%s updated", updated.ID),
	}
	if updated.Status == TodoDone {
		result["verification_needed"] = true
		result["hint"] = "Verify the result before proceeding to the next item."
	}
	return json.Marshal(result)
}

// ---------------------------------------------------------------------------
// TodoListTool
// ---------------------------------------------------------------------------

// TodoListTool lists all todo items.
type TodoListTool struct{ store *TodoStore }

func (t *TodoListTool) Name() string  { return "todo_list" }
func (t *TodoListTool) Label() string { return "List Todo Items" }
func (t *TodoListTool) Description() string {
	return "List all todo items and their current status."
}
func (t *TodoListTool) Schema() map[string]any { return schema.Object() }

func (t *TodoListTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	snap := t.store.Snapshot()
	if snap.Total == 0 {
		return json.Marshal("No todo items")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Todo items: %d total (%d pending, %d in_progress, %d done)\n",
		snap.Total, snap.Pending, snap.InProgress, snap.Done)
	for _, item := range snap.Items {
		line := fmt.Sprintf("- #%s [%s] %s", item.ID, item.Status, item.Title)
		if item.Owner != "" {
			line += fmt.Sprintf(" (owner: %s)", item.Owner)
		}
		if len(item.DependsOn) > 0 {
			line += fmt.Sprintf(" [depends on: %s]", strings.Join(item.DependsOn, ", "))
		}
		sb.WriteString(line + "\n")
	}
	return json.Marshal(strings.TrimRight(sb.String(), "\n"))
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewTodoTools creates a TodoStore and the four todo tools that share it.
func NewTodoTools() (*TodoStore, []agentcore.Tool) {
	store := NewTodoStore()
	return store, []agentcore.Tool{
		&TodoCreateTool{store: store},
		&TodoGetTool{store: store},
		&TodoUpdateTool{store: store},
		&TodoListTool{store: store},
	}
}
