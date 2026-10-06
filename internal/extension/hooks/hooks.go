package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/infra/config"
)

// EventType identifies when a hook fires.
type EventType string

const (
	PreToolUse         EventType = "PreToolUse"
	PostToolUse        EventType = "PostToolUse"
	Notification       EventType = "Notification"
	PostStopValidation EventType = "PostStopValidation"
	SessionStart       EventType = "SessionStart"
	SessionEnd         EventType = "SessionEnd"
	UserPromptSubmit   EventType = "UserPromptSubmit"
)

const defaultTimeout = 60 * time.Second

// Payload is the JSON written to the hook command's stdin.
type Payload struct {
	Event   EventType       `json:"event"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Message string          `json:"message,omitempty"`
	Prompt  string          `json:"prompt,omitempty"` // UserPromptSubmit
}

// hookOutput is the JSON a hook may print to stdout to influence the run:
// block applies to blocking PreToolUse and UserPromptSubmit hooks,
// updated_input to PreToolUse, additional_context to UserPromptSubmit.
type hookOutput struct {
	Block             bool            `json:"block,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	AdditionalContext string          `json:"additional_context,omitempty"`
	UpdatedInput      json.RawMessage `json:"updated_input,omitempty"`
}

// Decision is what the hooks that let a call or prompt through add to it; a
// hook that blocks is an error instead.
type Decision struct {
	AdditionalContext string
	UpdatedInput      json.RawMessage
}

// evalResult is a single hook's evaluated outcome.
type evalResult struct {
	decision  Decision
	blocked   bool
	reason    string
	err       error  // non-blocking execution error, for logging
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
	env       []string // the hook's own environment, as K=V
}

// Runner runs a conversation's hooks. They are the user's own or a trusted
// project's, so they run unasked; Set replaces them as the extensions
// reload.
type Runner struct {
	sessionID string
	model     func() agentcore.Model
	hooks     atomic.Pointer[map[EventType][]entry]
}

// New returns the runner of a conversation's hooks, none until Set. Prompt
// hooks ask whatever model returns at the time they run.
func New(sessionID string, model func() agentcore.Model) *Runner {
	r := &Runner{sessionID: sessionID, model: model}
	r.Set(nil)
	return r
}

// Set compiles cfg into the hooks that run from now on.
func (r *Runner) Set(cfg config.HooksConfig) {
	hooks := compileConfig(cfg, r.model)
	r.hooks.Store(&hooks)
}

// preToolUse evaluates PreToolUse hooks. A blocking hook that signals a block
// returns an error; otherwise the Decision may carry rewritten arguments.
func (r *Runner) preToolUse(ctx context.Context, toolName string, args json.RawMessage) (Decision, error) {
	return r.evaluate(ctx, toolName, args, Payload{Event: PreToolUse, Tool: toolName, Args: args})
}

// postToolUse fires the matching PostToolUse hooks in the background.
func (r *Runner) postToolUse(toolName string, args, output json.RawMessage, isError bool) {
	r.fireAsync(toolName, args, Payload{Event: PostToolUse, Tool: toolName, Args: args, Output: output, IsError: isError})
}

// RunNotification fires the Notification hooks in the background.
func (r *Runner) RunNotification(message string) {
	r.fireAsync("", nil, Payload{Event: Notification, Message: message})
}

// runPostStopValidation executes matching PostStopValidation hooks synchronously.
// Returns the output of the first failing hook (non-zero exit or exit-2
// block), or "" when every validation passes.
func (r *Runner) runPostStopValidation(ctx context.Context) (failOutput string) {
	payload := Payload{Event: PostStopValidation, Message: "post-stop validation"}
	for _, e := range r.matching(PostStopValidation, "", nil) {
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

// RunSessionStart fires the SessionStart hooks in the background.
func (r *Runner) RunSessionStart() { r.fireAsync("", nil, Payload{Event: SessionStart}) }

// RunSessionEnd fires the SessionEnd hooks in the background.
func (r *Runner) RunSessionEnd() { r.fireAsync("", nil, Payload{Event: SessionEnd}) }

// RunUserPromptSubmit evaluates UserPromptSubmit hooks. A blocking hook
// rejects the prompt with an error; otherwise the returned Decision may carry
// additional context to prepend to the turn.
func (r *Runner) RunUserPromptSubmit(ctx context.Context, prompt string) (Decision, error) {
	return r.evaluate(ctx, "", nil, Payload{Event: UserPromptSubmit, Prompt: prompt})
}

// fireAsync runs the hooks matching payload's event, tool and arguments in
// the background. They outlive the call that fired them; each is bounded by
// its own timeout.
func (r *Runner) fireAsync(toolName string, args json.RawMessage, payload Payload) {
	for _, e := range r.matching(payload.Event, toolName, args) {
		go func() {
			if res := r.runOne(context.Background(), e, payload); res.err != nil {
				log.Printf("hooks: %s %q: %v", payload.Event, e.label, res.err)
			}
		}()
	}
}

// matching returns the event's entries whose matcher accepts the tool name
// and whose if-condition, if any, accepts the arguments.
func (r *Runner) matching(event EventType, toolName string, args json.RawMessage) []entry {
	var result []entry
	for _, e := range (*r.hooks.Load())[event] {
		if e.matcher.Match(toolName) && (e.argFilter == nil || e.argFilter.Match(string(args))) {
			result = append(result, e)
		}
	}
	return result
}

func compileConfig(cfg config.HooksConfig, model func() agentcore.Model) map[EventType][]entry {
	hooks := make(map[EventType][]entry)
	for event, entries := range cfg {
		for _, he := range entries {
			// The extensions checked the hooks as they loaded; see Check.
			if e, err := compile(he, model); err == nil {
				hooks[EventType(event)] = append(hooks[EventType(event)], e)
			}
		}
	}
	return hooks
}

// Check checks a hook of event as settings give it: an event that fires, a
// type that runs, what the type needs, and matchers that compile.
func Check(event string, he config.HookEntry) error {
	switch EventType(event) {
	case PreToolUse, PostToolUse, Notification, PostStopValidation,
		SessionStart, SessionEnd, UserPromptSubmit:
	default:
		return fmt.Errorf("unknown event %q", event)
	}
	_, err := compile(he, nil)
	return err
}

// compile makes he ready to run.
func compile(he config.HookEntry, model func() agentcore.Model) (entry, error) {
	exec, label, err := buildExecutor(he, model)
	if err != nil {
		return entry{}, err
	}
	e := entry{exec: exec, label: label, timeout: defaultTimeout}
	if e.matcher, err = parseMatcher(he.Matcher); err != nil {
		return entry{}, fmt.Errorf("bad matcher %q: %w", he.Matcher, err)
	}
	if he.If != "" {
		if e.argFilter, err = parseMatcher(he.If); err != nil {
			return entry{}, fmt.Errorf("bad if-condition %q: %w", he.If, err)
		}
	}
	if he.Blocking != nil {
		e.blocking = *he.Blocking
	}
	if he.Timeout != nil && *he.Timeout > 0 {
		e.timeout = time.Duration(*he.Timeout) * time.Second
	}
	for _, k := range slices.Sorted(maps.Keys(he.Env)) {
		e.env = append(e.env, k+"="+he.Env[k])
	}
	return e, nil
}

func buildExecutor(he config.HookEntry, model func() agentcore.Model) (executor, string, error) {
	switch he.Type {
	case "command":
		if he.Command == "" {
			return nil, "", errors.New("a command hook needs a command")
		}
		c, err := commandFor(he, runtime.GOOS)
		if err != nil {
			return nil, "", err
		}
		return c, c.command, nil
	case "prompt":
		if he.Prompt == "" {
			return nil, "", errors.New("a prompt hook needs a prompt")
		}
		return &promptExec{prompt: he.Prompt, model: model}, "prompt:" + truncate(he.Prompt, 40), nil
	case "http":
		if he.URL == "" {
			return nil, "", errors.New("an http hook needs a url")
		}
		return &httpExec{url: he.URL, headers: he.Headers}, "http:" + he.URL, nil
	}
	return nil, "", fmt.Errorf("unknown type %q", he.Type)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// runOne runs the hook and evaluates its outcome.
func (r *Runner) runOne(ctx context.Context, e entry, payload Payload) evalResult {
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

// evaluate runs the hooks matching payload's event, tool and arguments in
// order. It stops at the first blocking hook that blocks, as an error, and
// otherwise merges what the hooks add.
func (r *Runner) evaluate(ctx context.Context, toolName string, args json.RawMessage, payload Payload) (Decision, error) {
	var merged Decision
	for _, e := range r.matching(payload.Event, toolName, args) {
		res := r.runOne(ctx, e, payload)
		if e.blocking && res.blocked {
			reason := res.reason
			if reason == "" {
				reason = "blocked by hook"
			}
			return merged, fmt.Errorf("hook: %s", reason)
		}
		if res.err != nil {
			log.Printf("hooks: %s %q: %v", payload.Event, e.label, res.err)
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

// execEntry runs the hook within its timeout, with the payload on stdin and
// the event in the environment.
func (r *Runner) execEntry(ctx context.Context, e entry, payload Payload) outcome {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	data, err := json.Marshal(payload)
	if err != nil {
		return outcome{exitCode: 1, err: fmt.Errorf("marshal hook payload: %w", err)}
	}

	env := append([]string{
		"HOOK_EVENT=" + string(payload.Event),
		"HOOK_TOOL_NAME=" + payload.Tool,
		"HOOK_SESSION_ID=" + r.sessionID,
	}, e.env...)

	return e.exec.execute(ctx, data, env)
}
