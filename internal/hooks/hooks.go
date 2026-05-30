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
	UserPromptSubmit   EventType = "UserPromptSubmit"
	SubagentStop       EventType = "SubagentStop"
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
	Prompt       string          `json:"prompt,omitempty"` // UserPromptSubmit
	Agent        string          `json:"agent,omitempty"`  // SubagentStop: teammate name
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

// hookOutput is the JSON a hook may print to stdout to influence the run.
type hookOutput struct {
	Block             bool            `json:"block,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	AdditionalContext string          `json:"additional_context,omitempty"`
	UpdatedInput      json.RawMessage `json:"updated_input,omitempty"`
}

// Decision is the normalized, allowed-with-extras result handed back to
// callers. A blocking hook is surfaced as an error by the Run* methods, so
// Decision only carries the payload that applies when the run proceeds.
type Decision struct {
	AdditionalContext string
	UpdatedInput      json.RawMessage
}

// evalResult is a single hook's evaluated outcome.
type evalResult struct {
	decision  Decision
	blocked   bool
	reason    string
	err       error  // non-blocking execution or approval error, for logging
	rawStdout []byte // raw hook stdout, used for PostStopValidation feedback
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

// RunPreToolUse evaluates PreToolUse hooks. A blocking hook that signals a
// block returns an error; otherwise the returned Decision may carry an updated
// tool input or additional context.
func (r *Runner) RunPreToolUse(ctx context.Context, toolName string, args json.RawMessage) (Decision, error) {
	return r.evaluate(ctx, PreToolUse, toolName, args, Payload{Event: PreToolUse, Tool: toolName, Args: args})
}

// RunPostToolUse fires matching PostToolUse hooks asynchronously.
// Uses a detached context so hooks survive parent cancellation.
func (r *Runner) RunPostToolUse(_ context.Context, toolName string, args, output json.RawMessage, isError bool) {
	payload := Payload{Event: PostToolUse, Tool: toolName, Args: args, Output: output, IsError: isError}
	for _, e := range r.matching(PostToolUse, toolName, args) {
		go func(e entry) {
			ctx, cancel := detachedHookContext(e.timeout)
			defer cancel()
			if res := r.runOne(ctx, e, payload); res.err != nil {
				log.Printf("hooks: PostToolUse %q: %v", e.label, res.err)
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
			if res := r.runOne(ctx, e, payload); res.err != nil {
				log.Printf("hooks: Notification %q: %v", e.label, res.err)
			}
		}(e)
	}
}

// RunPostStopValidation executes matching PostStopValidation hooks synchronously.
// Returns the output of the first failing hook (non-zero exit, exit-2 block, or
// approval denial), or "" when every validation passes.
func (r *Runner) RunPostStopValidation(ctx context.Context) (failOutput string) {
	payload := Payload{Event: PostStopValidation, Message: "post-stop validation"}
	for _, e := range r.matching(PostStopValidation, "") {
		res := r.runOne(ctx, e, payload)
		if !res.blocked && res.err == nil {
			continue
		}
		if msg := strings.TrimSpace(string(res.rawStdout)); msg != "" {
			return msg
		}
		if res.reason != "" {
			return res.reason
		}
		if res.err != nil {
			return res.err.Error()
		}
		return "post-stop validation failed"
	}
	return ""
}

// RunTaskCreated executes TaskCreated hooks synchronously.
// Any blocking hook aborts the task creation so callers can roll back.
func (r *Runner) RunTaskCreated(ctx context.Context, task TaskSnapshot) error {
	_, err := r.evaluate(ctx, TaskCreated, "task_create", nil, Payload{
		Event: TaskCreated,
		Tool:  "task_create",
		Task:  &task,
	})
	return err
}

// RunTaskCompleted executes TaskCompleted hooks synchronously.
// Any blocking hook aborts the completion transition.
func (r *Runner) RunTaskCompleted(ctx context.Context, previous, current TaskSnapshot) error {
	_, err := r.evaluate(ctx, TaskCompleted, "task_update", nil, Payload{
		Event:        TaskCompleted,
		Tool:         "task_update",
		Task:         &current,
		PreviousTask: &previous,
		StatusFrom:   previous.Status,
		StatusTo:     current.Status,
	})
	return err
}

// RunSessionStart fires SessionStart hooks asynchronously.
func (r *Runner) RunSessionStart(ctx context.Context) {
	r.fireAsync(SessionStart, "", Payload{Event: SessionStart})
}

// RunSessionEnd fires SessionEnd hooks asynchronously.
func (r *Runner) RunSessionEnd(ctx context.Context) {
	r.fireAsync(SessionEnd, "", Payload{Event: SessionEnd})
}

// RunSubagentStop fires SubagentStop hooks asynchronously when a teammate's
// agent loop exits. The hook matcher is tested against the teammate name (an
// empty matcher fires for every teammate). Observation only — by the time it
// runs the teammate has already exited, so it cannot keep it alive.
func (r *Runner) RunSubagentStop(_ context.Context, agentName string) {
	r.fireAsync(SubagentStop, agentName, Payload{Event: SubagentStop, Agent: agentName})
}

// RunUserPromptSubmit evaluates UserPromptSubmit hooks. A blocking hook
// rejects the prompt with an error; otherwise the returned Decision may carry
// additional context to prepend to the turn.
func (r *Runner) RunUserPromptSubmit(ctx context.Context, prompt string) (Decision, error) {
	return r.evaluate(ctx, UserPromptSubmit, "", nil, Payload{Event: UserPromptSubmit, Prompt: prompt})
}

// fireAsync runs all matching hooks for the event in background goroutines.
// matchName is tested against each hook's matcher (empty = match all).
func (r *Runner) fireAsync(event EventType, matchName string, payload Payload) {
	for _, e := range r.matching(event, matchName) {
		go func(e entry) {
			ctx, cancel := detachedHookContext(e.timeout)
			defer cancel()
			if res := r.runOne(ctx, e, payload); res.err != nil {
				log.Printf("hooks: %s %q: %v", event, e.label, res.err)
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
		TaskCreated, TaskCompleted, SubagentStop,
		SessionStart, SessionEnd, UserPromptSubmit:
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

// runOne applies the approval gate then runs the hook, returning its evaluated
// result. An approval denial counts as a block.
func (r *Runner) runOne(ctx context.Context, e entry, payload Payload) evalResult {
	if r.approval != nil {
		if err := r.approval.ApproveHook(ctx, approval.HookRequest{
			Event:    string(payload.Event),
			Tool:     payload.Tool,
			Command:  e.label,
			Blocking: e.blocking,
		}); err != nil {
			return evalResult{blocked: true, reason: err.Error(), err: err}
		}
	}
	return interpret(r.execEntry(ctx, e, payload))
}

// interpret normalizes a hook's outcome. A hook blocks when it prints
// {"block":true} or (for command hooks) exits with code 2. Any other non-zero
// exit or transport error is a non-blocking error: it is logged but does not
// stop the run.
func interpret(o outcome) evalResult {
	var out hookOutput
	if len(o.stdout) > 0 {
		_ = json.Unmarshal(o.stdout, &out) // non-JSON stdout is ignored
	}

	res := evalResult{
		decision:  Decision{AdditionalContext: out.AdditionalContext},
		reason:    out.Reason,
		rawStdout: o.stdout,
	}
	if len(out.UpdatedInput) > 0 && json.Valid(out.UpdatedInput) {
		res.decision.UpdatedInput = out.UpdatedInput
	}
	if out.Block || o.exitCode == 2 {
		res.blocked = true
		return res
	}
	res.err = o.err
	return res
}

// evaluate runs every matching hook for an event in order. It short-circuits
// on the first blocking hook (surfaced as an error) and otherwise merges the
// allowed hooks' Decision payloads.
func (r *Runner) evaluate(ctx context.Context, event EventType, toolName string, args json.RawMessage, payload Payload) (Decision, error) {
	var merged Decision
	for _, e := range r.matching(event, toolName, args) {
		res := r.runOne(ctx, e, payload)
		if e.blocking && res.blocked {
			reason := res.reason
			if reason == "" {
				reason = "blocked by hook"
			}
			return merged, fmt.Errorf("hook: %s", reason)
		}
		if res.err != nil {
			log.Printf("hooks: %s %q: %v", event, e.label, res.err)
		}
		merged = mergeDecision(merged, res.decision)
	}
	return merged, nil
}

// mergeDecision combines two hook decisions: additional contexts are joined and
// a later updated input overrides an earlier one.
func mergeDecision(a, b Decision) Decision {
	if b.AdditionalContext != "" {
		if a.AdditionalContext == "" {
			a.AdditionalContext = b.AdditionalContext
		} else {
			a.AdditionalContext += "\n\n" + b.AdditionalContext
		}
	}
	if len(b.UpdatedInput) > 0 {
		a.UpdatedInput = b.UpdatedInput
	}
	return a
}

func detachedHookContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

// execEntry dispatches the hook to its executor with proper context and env.
func (r *Runner) execEntry(ctx context.Context, e entry, payload Payload) outcome {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	if r.model != nil {
		ctx = withModel(ctx, r.model)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return outcome{exitCode: 1, err: fmt.Errorf("marshal hook payload: %w", err)}
	}

	env := []string{
		"HOOK_EVENT=" + string(payload.Event),
		"HOOK_TOOL_NAME=" + payload.Tool,
		"HOOK_SESSION_ID=" + r.sessionID,
	}

	return e.exec.execute(ctx, data, env)
}
