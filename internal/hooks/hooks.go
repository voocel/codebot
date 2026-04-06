package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/voocel/agentcore"
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
	SessionStart       EventType = "SessionStart"
	SessionEnd         EventType = "SessionEnd"
	PreCompact         EventType = "PreCompact"
	PostCompact        EventType = "PostCompact"
	UserPromptSubmit   EventType = "UserPromptSubmit"
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
	Prompt       string          `json:"prompt,omitempty"`        // UserPromptSubmit
	Reason       string          `json:"reason,omitempty"`        // Pre/PostCompact
	TokensBefore int             `json:"tokens_before,omitempty"` // Pre/PostCompact
	TokensAfter  int             `json:"tokens_after,omitempty"`  // PostCompact
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
	exec      executor
	label     string // human-readable identifier for logging
	matcher   matcher
	argFilter matcher // if-condition: matches against tool args JSON; nil = no filter
	blocking  bool
	timeout   time.Duration
}

// Runner manages and executes hooks.
type Runner struct {
	hooks     map[EventType][]entry
	sessionID string
	approval  *approval.Engine
	model     agentcore.ChatModel // for prompt hooks; may be nil
}

// New parses a HooksConfig and returns a Runner.
// Returns nil if no valid hooks are found.
// The model parameter is used for prompt-type hooks and may be nil.
func New(cfg config.HooksConfig, sessionID string, engine *approval.Engine, model agentcore.ChatModel) *Runner {
	hooks := compileConfig(cfg)

	if len(hooks) == 0 {
		return nil
	}
	return &Runner{hooks: hooks, sessionID: sessionID, approval: engine, model: model}
}

// RunPreToolUse executes all matching PreToolUse hooks.
// A blocking hook that exits non-zero or returns {"blocked":true} returns an error.
func (r *Runner) RunPreToolUse(ctx context.Context, toolName string, args json.RawMessage) error {
	payload := Payload{Event: PreToolUse, Tool: toolName, Args: args}
	for _, e := range r.matching(PreToolUse, toolName, args) {
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
			log.Printf("hooks: PreToolUse %q: %v", e.label, err)
		}
	}
	return nil
}

// RunPostToolUse fires matching PostToolUse hooks asynchronously.
// Uses a detached context so hooks survive parent cancellation.
func (r *Runner) RunPostToolUse(_ context.Context, toolName string, args, output json.RawMessage, isError bool) {
	payload := Payload{Event: PostToolUse, Tool: toolName, Args: args, Output: output, IsError: isError}
	for _, e := range r.matching(PostToolUse, toolName, args) {
		go func(e entry) {
			ctx, cancel := detachedHookContext(e.timeout)
			defer cancel()
			if _, err := r.run(ctx, e, payload); err != nil {
				log.Printf("hooks: PostToolUse %q: %v", e.label, err)
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
				log.Printf("hooks: Notification %q: %v", e.label, err)
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

// RunSessionStart fires SessionStart hooks asynchronously.
func (r *Runner) RunSessionStart(ctx context.Context) {
	r.fireAsync(SessionStart, Payload{Event: SessionStart})
}

// RunSessionEnd fires SessionEnd hooks asynchronously.
func (r *Runner) RunSessionEnd(ctx context.Context) {
	r.fireAsync(SessionEnd, Payload{Event: SessionEnd})
}

// RunPreCompact fires PreCompact hooks asynchronously.
func (r *Runner) RunPreCompact(ctx context.Context, reason string, tokensBefore int) {
	r.fireAsync(PreCompact, Payload{Event: PreCompact, Reason: reason, TokensBefore: tokensBefore})
}

// RunPostCompact fires PostCompact hooks asynchronously.
func (r *Runner) RunPostCompact(ctx context.Context, reason string, tokensBefore, tokensAfter int) {
	r.fireAsync(PostCompact, Payload{Event: PostCompact, Reason: reason, TokensBefore: tokensBefore, TokensAfter: tokensAfter})
}

// RunUserPromptSubmit executes UserPromptSubmit hooks synchronously.
// A blocking hook that returns {"blocked":true} rejects the user's input.
func (r *Runner) RunUserPromptSubmit(ctx context.Context, prompt string) error {
	payload := Payload{Event: UserPromptSubmit, Prompt: prompt}
	for _, e := range r.matching(UserPromptSubmit, "") {
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
		}
		if err != nil {
			log.Printf("hooks: UserPromptSubmit %q: %v", e.label, err)
		}
	}
	return nil
}

// fireAsync runs all matching hooks for the event in background goroutines.
func (r *Runner) fireAsync(event EventType, payload Payload) {
	for _, e := range r.matching(event, "") {
		go func(e entry) {
			ctx, cancel := detachedHookContext(e.timeout)
			defer cancel()
			if _, err := r.run(ctx, e, payload); err != nil {
				log.Printf("hooks: %s %q: %v", event, e.label, err)
			}
		}(e)
	}
}

// matching returns entries for the given event that match the tool name
// and optionally the tool arguments (if an argFilter is configured).
func (r *Runner) matching(event EventType, toolName string, args ...json.RawMessage) []entry {
	argsStr := ""
	if len(args) > 0 && len(args[0]) > 0 {
		argsStr = string(args[0])
	}
	match := func(e entry) bool {
		if !e.matcher.Match(toolName) {
			return false
		}
		if e.argFilter != nil && !e.argFilter.Match(argsStr) {
			return false
		}
		return true
	}

	var result []entry
	for _, e := range r.hooks[event] {
		if match(e) {
			result = append(result, e)
		}
	}
	return result
}

func compileConfig(cfg config.HooksConfig) map[EventType][]entry {
	hooks := make(map[EventType][]entry)
	for event, entries := range cfg {
		et := EventType(event)
		if !isKnownEvent(et) {
			log.Printf("hooks: unknown event %q, skipped", event)
			continue
		}
		for _, he := range entries {
			exec, label := buildExecutor(he)
			if exec == nil {
				continue
			}
			m, err := parseMatcher(he.Matcher)
			if err != nil {
				log.Printf("hooks: bad matcher %q: %v, skipped", he.Matcher, err)
				continue
			}
			var af matcher
			if he.If != "" {
				af, err = parseMatcher(he.If)
				if err != nil {
					log.Printf("hooks: bad if-condition %q: %v, skipped", he.If, err)
					continue
				}
			}
			e := entry{
				exec:      exec,
				label:     label,
				matcher:   m,
				argFilter: af,
				timeout:   defaultTimeout,
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

func isKnownEvent(et EventType) bool {
	switch et {
	case PreToolUse, PostToolUse, Notification, PostStopValidation,
		TaskCreated, TaskCompleted,
		SessionStart, SessionEnd, PreCompact, PostCompact, UserPromptSubmit:
		return true
	}
	return false
}

func buildExecutor(he config.HookEntry) (executor, string) {
	switch he.Type {
	case "command":
		if he.Command == "" {
			return nil, ""
		}
		return &commandExec{command: he.Command}, he.Command
	case "prompt":
		if he.Prompt == "" {
			return nil, ""
		}
		label := "prompt:" + truncate(he.Prompt, 40)
		return &promptExec{prompt: he.Prompt}, label
	case "http":
		if he.URL == "" {
			return nil, ""
		}
		return &httpExec{url: he.URL, headers: he.Headers}, "http:" + he.URL
	default:
		if he.Type != "" {
			log.Printf("hooks: unknown type %q, skipped", he.Type)
		}
		return nil, ""
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
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
			log.Printf("hooks: %s %q: %v", event, e.label, err)
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
			Command:  e.label,
			Blocking: e.blocking,
		}); err != nil {
			return nil, err
		}
	}
	return r.execEntry(ctx, e, payload)
}

func detachedHookContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

// execEntry dispatches the hook to its executor with proper context and env.
func (r *Runner) execEntry(ctx context.Context, e entry, payload Payload) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	if r.model != nil {
		ctx = withModel(ctx, r.model)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal hook payload: %w", err)
	}

	env := []string{
		"HOOK_EVENT=" + string(payload.Event),
		"HOOK_TOOL_NAME=" + payload.Tool,
		"HOOK_SESSION_ID=" + r.sessionID,
	}

	return e.exec.execute(ctx, data, env)
}
