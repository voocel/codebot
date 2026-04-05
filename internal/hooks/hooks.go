package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
)

// EventType identifies when a hook fires.
type EventType string

const (
	PreToolUse         EventType = "PreToolUse"
	PostToolUse        EventType = "PostToolUse"
	Notification       EventType = "Notification"
	PostStopValidation EventType = "PostStopValidation"
	TaskCreated        EventType = "TaskCreated"
	TaskCompleted      EventType = "TaskCompleted"
)

const defaultTimeout = 60 * time.Second

// Payload is the JSON written to the hook command's stdin.
type Payload struct {
	Event        EventType       `json:"event"`
	Tool         string          `json:"tool,omitempty"`
	Args         json.RawMessage `json:"args,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Message      string          `json:"message,omitempty"`
	Task         *TaskSnapshot   `json:"task,omitempty"`
	PreviousTask *TaskSnapshot   `json:"previous_task,omitempty"`
	StatusFrom   string          `json:"status_from,omitempty"`
	StatusTo     string          `json:"status_to,omitempty"`
}

// TaskSnapshot is the task payload exposed to lifecycle hooks.
type TaskSnapshot struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description,omitempty"`
	ActiveForm  string         `json:"active_form,omitempty"`
	Status      string         `json:"status"`
	Owner       string         `json:"owner,omitempty"`
	Blocks      []string       `json:"blocks,omitempty"`
	BlockedBy   []string       `json:"blocked_by,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Result is the expected JSON structure from a blocking hook's stdout.
type Result struct {
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason,omitempty"`
}

// entry is a compiled, ready-to-run hook.
type entry struct {
	command  string
	matcher  matcher
	blocking bool
	timeout  time.Duration
}

// Runner manages and executes hooks.
type Runner struct {
	hooks     map[EventType][]entry
	sessionID string
	approval  *approval.Engine
	dynamic   func() config.HooksConfig
}

// New parses a HooksConfig and returns a Runner.
// Returns nil if no valid hooks are found.
func New(cfg config.HooksConfig, sessionID string, engine *approval.Engine) *Runner {
	hooks := compileConfig(cfg)

	if len(hooks) == 0 {
		return nil
	}
	return &Runner{hooks: hooks, sessionID: sessionID, approval: engine}
}

func (r *Runner) SetDynamicProvider(fn func() config.HooksConfig) {
	r.dynamic = fn
}

// RunPreToolUse executes all matching PreToolUse hooks.
// A blocking hook that exits non-zero or returns {"blocked":true} returns an error.
func (r *Runner) RunPreToolUse(ctx context.Context, toolName string, args json.RawMessage) error {
	payload := Payload{Event: PreToolUse, Tool: toolName, Args: args}
	for _, e := range r.matching(PreToolUse, toolName) {
		stdout, err := r.run(ctx, e, payload)
		if e.blocking {
			if err != nil {
				// Check if stdout contains a JSON block message.
				var res Result
				if json.Unmarshal(stdout, &res) == nil && res.Blocked {
					reason := res.Reason
					if reason == "" {
						reason = "blocked by hook"
					}
					return fmt.Errorf("hook: %s", reason)
				}
				return fmt.Errorf("hook: %v", err)
			}
			// Even on success, check if stdout explicitly blocks.
			var res Result
			if json.Unmarshal(stdout, &res) == nil && res.Blocked {
				reason := res.Reason
				if reason == "" {
					reason = "blocked by hook"
				}
				return fmt.Errorf("hook: %s", reason)
			}
		}
		if err != nil {
			log.Printf("hooks: PreToolUse %q: %v", e.command, err)
		}
	}
	return nil
}

// RunPostToolUse fires matching PostToolUse hooks asynchronously.
// Uses a detached context so hooks survive parent cancellation.
func (r *Runner) RunPostToolUse(_ context.Context, toolName string, args, output json.RawMessage, isError bool) {
	payload := Payload{Event: PostToolUse, Tool: toolName, Args: args, Output: output, IsError: isError}
	for _, e := range r.matching(PostToolUse, toolName) {
		go func(e entry) {
			ctx, cancel := detachedHookContext(e.timeout)
			defer cancel()
			if _, err := r.run(ctx, e, payload); err != nil {
				log.Printf("hooks: PostToolUse %q: %v", e.command, err)
			}
		}(e)
	}
}

// RunNotification fires matching Notification hooks asynchronously.
// Uses a detached context so hooks survive parent cancellation.
func (r *Runner) RunNotification(_ context.Context, message string) {
	payload := Payload{Event: Notification, Message: message}
	for _, e := range r.matching(Notification, "") {
		go func(e entry) {
			ctx, cancel := detachedHookContext(e.timeout)
			defer cancel()
			if _, err := r.run(ctx, e, payload); err != nil {
				log.Printf("hooks: Notification %q: %v", e.command, err)
			}
		}(e)
	}
}

// RunPostStopValidation executes matching PostStopValidation hooks synchronously.
// Returns the combined stderr/error output of the first failing hook, or "" on success.
func (r *Runner) RunPostStopValidation(ctx context.Context) (failOutput string) {
	payload := Payload{Event: PostStopValidation, Message: "post-stop validation"}
	for _, e := range r.matching(PostStopValidation, "") {
		stdout, err := r.run(ctx, e, payload)
		if err != nil {
			msg := strings.TrimSpace(string(stdout))
			if msg == "" {
				msg = err.Error()
			}
			return msg
		}
	}
	return ""
}

// RunTaskCreated executes TaskCreated hooks synchronously.
// Any hook error aborts the task creation so callers can roll back.
func (r *Runner) RunTaskCreated(ctx context.Context, task TaskSnapshot) error {
	return r.runLifecycleHooks(ctx, TaskCreated, "task_create", Payload{
		Event: TaskCreated,
		Tool:  "task_create",
		Task:  &task,
	})
}

// RunTaskCompleted executes TaskCompleted hooks synchronously.
// Any hook error aborts the completion transition.
func (r *Runner) RunTaskCompleted(ctx context.Context, previous, current TaskSnapshot) error {
	return r.runLifecycleHooks(ctx, TaskCompleted, "task_update", Payload{
		Event:        TaskCompleted,
		Tool:         "task_update",
		Task:         &current,
		PreviousTask: &previous,
		StatusFrom:   previous.Status,
		StatusTo:     current.Status,
	})
}

// matching returns entries for the given event that match the tool name.
func (r *Runner) matching(event EventType, toolName string) []entry {
	var result []entry
	for _, e := range r.hooks[event] {
		if e.matcher.Match(toolName) {
			result = append(result, e)
		}
	}
	if r.dynamic != nil {
		for _, e := range compileConfig(r.dynamic())[event] {
			if e.matcher.Match(toolName) {
				result = append(result, e)
			}
		}
	}
	return result
}

func compileConfig(cfg config.HooksConfig) map[EventType][]entry {
	hooks := make(map[EventType][]entry)
	for event, entries := range cfg {
		et := EventType(event)
		if et != PreToolUse &&
			et != PostToolUse &&
			et != Notification &&
			et != PostStopValidation &&
			et != TaskCreated &&
			et != TaskCompleted {
			log.Printf("hooks: unknown event %q, skipped", event)
			continue
		}
		for _, he := range entries {
			if he.Type != "command" || he.Command == "" {
				continue
			}
			m, err := parseMatcher(he.Matcher)
			if err != nil {
				log.Printf("hooks: bad matcher %q: %v, skipped", he.Matcher, err)
				continue
			}
			e := entry{
				command: he.Command,
				matcher: m,
				timeout: defaultTimeout,
			}
			if he.Blocking != nil {
				e.blocking = *he.Blocking
			}
			if he.Timeout != nil && *he.Timeout > 0 {
				e.timeout = time.Duration(*he.Timeout) * time.Second
			}
			hooks[et] = append(hooks[et], e)
		}
	}
	return hooks
}

func (r *Runner) runLifecycleHooks(ctx context.Context, event EventType, source string, payload Payload) error {
	for _, e := range r.matching(event, source) {
		stdout, err := r.run(ctx, e, payload)
		if e.blocking {
			if err != nil {
				var res Result
				if json.Unmarshal(stdout, &res) == nil && res.Blocked {
					reason := res.Reason
					if reason == "" {
						reason = "blocked by hook"
					}
					return fmt.Errorf("hook: %s", reason)
				}
				return fmt.Errorf("hook: %v", err)
			}

			var res Result
			if json.Unmarshal(stdout, &res) == nil && res.Blocked {
				reason := res.Reason
				if reason == "" {
					reason = "blocked by hook"
				}
				return fmt.Errorf("hook: %s", reason)
			}
			continue
		}

		if err != nil {
			log.Printf("hooks: %s %q: %v", event, e.command, err)
			continue
		}
	}
	return nil
}

func (r *Runner) run(ctx context.Context, e entry, payload Payload) ([]byte, error) {
	if r.approval != nil {
		if err := r.approval.ApproveHook(ctx, approval.HookRequest{
			Event:    string(payload.Event),
			Tool:     payload.Tool,
			Command:  e.command,
			Blocking: e.blocking,
		}); err != nil {
			return nil, err
		}
	}
	return r.execCommand(ctx, e, payload)
}

func detachedHookContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

// execCommand runs a hook command with the payload on stdin.
func (r *Runner) execCommand(ctx context.Context, e entry, payload Payload) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", e.command)
	cmd.Env = append(cmd.Environ(),
		"HOOK_EVENT="+string(payload.Event),
		"HOOK_TOOL_NAME="+payload.Tool,
		"HOOK_SESSION_ID="+r.sessionID,
	)

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal hook payload: %w", err)
	}
	cmd.Stdin = bytes.NewReader(data)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("command %q: %w (stderr: %s)", e.command, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
