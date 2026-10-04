// Package print is the non-interactive frontend (-p): one prompt in, the
// reply on stdout, or every event as JSON lines with -json.
package print

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
)

// UI is the user in print mode, where nobody can answer: whatever the
// permission mode does not allow on its own is refused.
type UI struct{}

var (
	errNobodyToApprove = errors.New("nobody can approve this in print mode; rerun with --mode accept-edits or --mode trust to allow it")
	// No mode reaches outside the workspace on its own.
	errOutsideWorkspace = errors.New("nobody can approve access outside the workspace in print mode; add the directory to permissions.read_roots or write_roots in settings.json to allow it")
)

func (UI) Approve(_ context.Context, req interact.Approval) (interact.Choice, error) {
	switch {
	case req.OutsideRoots:
		return interact.Deny, errOutsideWorkspace
	case req.OnceOnly:
		// No mode allows it either: it is confirmed every time.
		return interact.Deny, fmt.Errorf("nobody can approve this in print mode: %s needs confirming each time", req.Reason)
	}
	return interact.Deny, errNobodyToApprove
}

func (UI) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}

// Run runs one prompt, from args or stdin, and returns once the
// conversation and its background tasks are done. The reply streams to
// stdout and tool and status notes to stderr; in JSON mode every agent event
// streams to stdout as JSONL.
func Run(a *app.App, args []string, jsonMode bool) error {
	prompt := strings.Join(args, " ")
	if prompt == "" {
		stdinPrompt, err := ReadStdinPrompt()
		if err != nil {
			return fmt.Errorf("stdin error: %w", err)
		}
		prompt = strings.TrimSpace(stdinPrompt)
	}
	if prompt == "" {
		return errors.New("print mode requires a prompt (argument or stdin pipe)")
	}

	// Unlike the TUI there is no later turn to pick MCP tools up, so connect
	// first.
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	report := a.ConnectMCP(ctx)
	cancel()
	for _, e := range report.Errors {
		fmt.Fprintf(os.Stderr, "mcp: %s\n", e)
	}

	p := &printer{json: jsonMode, hidden: make(map[string]bool)}
	unsubscribe := a.Subscribe(p.onEvent)
	defer unsubscribe()

	conv := a.Current()
	if err := conv.Submit(context.Background(), []litellm.Block{litellm.Text(prompt)}); err != nil {
		return fmt.Errorf("print mode: %w", err)
	}
	// A background task posts its result before it counts as done, and the
	// result starts another run, which may start more tasks. Done is when
	// the tasks are finished and handling what they posted ran nothing.
	for {
		conv.Tasks().Wait()
		last := conv.Status().LastRun
		if err := conv.Wait(context.Background()); err != nil {
			return err
		}
		if conv.Status().LastRun == last {
			break
		}
	}
	return p.failure()
}

// printer writes the events of a print run.
type printer struct {
	json   bool
	hidden map[string]bool // tool calls the transcript does not show

	mu  sync.Mutex
	err error // the first failure
}

func (p *printer) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
}

func (p *printer) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *printer) onEvent(ev app.Event) {
	if ev.Kind != app.SessionEvent {
		return
	}
	switch ev.Session.Kind {
	case session.Error:
		fmt.Fprintf(os.Stderr, "session error: %v\n", ev.Session.Err)
		p.fail(ev.Session.Err)
	case session.Agent:
		p.onAgentEvent(ev.Session.Agent)
	}
}

func (p *printer) onAgentEvent(ev agentcore.Event) {
	if e, ok := ev.(agentcore.RunEnd); ok && e.Err != nil && !errors.Is(e.Err, context.Canceled) {
		p.fail(e.Err)
	}
	if p.json {
		if v := jsonFor(ev); v != nil {
			data, _ := json.Marshal(v)
			fmt.Fprintln(os.Stdout, string(data))
		}
		return
	}

	switch e := ev.(type) {
	case agentcore.MessageDelta:
		// Only answer text: thinking and tool-call argument deltas are not
		// part of the reply.
		if d, ok := e.Event.(litellm.TextDelta); ok {
			fmt.Fprint(os.Stdout, d.Text)
		}
	case agentcore.MessageEnd:
		// End the reply's line, so tool notes on stderr start their own.
		if text := e.Message.Text(); e.Message.Role == litellm.RoleAssistant && text != "" && !strings.HasSuffix(text, "\n") {
			fmt.Fprintln(os.Stdout)
		}
	case agentcore.ToolStart:
		if app.HiddenToolCall(e.Call.Name, e.Call.Args) {
			p.hidden[e.Call.ID] = true
			return
		}
		fmt.Fprintf(os.Stderr, "[tool] %s\n", e.Call.Name)
	case agentcore.ToolEnd:
		if p.hidden[e.Call.ID] {
			delete(p.hidden, e.Call.ID)
			return
		}
		if e.Result.IsError {
			fmt.Fprintf(os.Stderr, "[tool] %s error\n", e.Call.Name)
		}
	case agentcore.Retry:
		fmt.Fprintf(os.Stderr, "request failed, retrying (attempt %d) in %s...\n", e.Attempt, e.Delay.Truncate(time.Millisecond))
	case agentcore.CompactionEnd:
		if e.Compaction != nil {
			fmt.Fprintf(os.Stderr, "context compacted: %d messages summarized\n", e.Compaction.Replaced)
		}
	case agentcore.RunEnd:
		if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "error: %v\n", e.Err)
		}
	}
}

// ReadStdinPrompt reads all of stdin as a prompt (for pipe usage).
func ReadStdinPrompt() (string, error) {
	info, _ := os.Stdin.Stat()
	if info.Mode()&os.ModeCharDevice != 0 {
		// Not piped, no stdin input.
		return "", nil
	}
	data, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}

// jsonFor is how -json prints an event: an object naming its type, with
// errors as their text, since an error value encodes as {}. Streamed deltas
// other than text, reasoning and tool arguments are left out, a sub-agent's
// too: the message they make up follows whole.
func jsonFor(ev agentcore.Event) map[string]any {
	switch e := ev.(type) {
	case agentcore.MessageStart:
		return map[string]any{"type": "message_start"}
	case agentcore.MessageDelta:
		switch d := e.Event.(type) {
		case litellm.TextDelta:
			return map[string]any{"type": "message_delta", "kind": "text", "index": d.Index, "delta": d.Text}
		case litellm.ReasoningDelta:
			return map[string]any{"type": "message_delta", "kind": "reasoning", "index": d.Index, "delta": d.Text}
		case litellm.ToolUseDelta:
			return map[string]any{"type": "message_delta", "kind": "tool_use", "index": d.Index, "delta": d.Arguments}
		}
		return nil
	case agentcore.MessageEnd:
		return map[string]any{"type": "message_end", "message": e.Message}
	case agentcore.ToolStart:
		ev := map[string]any{"type": "tool_start", "id": e.Call.ID, "name": e.Call.Name, "args": e.Call.Args}
		if e.Call.Preview != "" {
			ev["preview"] = e.Call.Preview
		}
		return ev
	case agentcore.ToolUpdate:
		progress := any(e.Progress)
		if sub, ok := e.Progress.(subagent.Progress); ok {
			event := jsonFor(sub.Event)
			if event == nil {
				return nil
			}
			progress = map[string]any{"agent": sub.Spawn.ID, "event": event}
		}
		return map[string]any{"type": "tool_update", "id": e.Call.ID, "name": e.Call.Name, "progress": progress}
	case agentcore.ToolEnd:
		return map[string]any{"type": "tool_end", "id": e.Call.ID, "name": e.Call.Name, "content": e.Result.Content, "is_error": e.Result.IsError}
	case agentcore.TurnEnd:
		return map[string]any{"type": "turn_end"}
	case agentcore.Retry:
		return map[string]any{"type": "retry", "attempt": e.Attempt, "delay_ms": e.Delay.Milliseconds(), "error": errText(e.Err)}
	case agentcore.CompactionStart:
		return map[string]any{"type": "compaction_start"}
	case agentcore.CompactionEnd:
		out := map[string]any{"type": "compaction_end", "error": errText(e.Err)}
		if e.Compaction != nil {
			out["replaced"] = e.Compaction.Replaced
			if e.Compaction.Usage != nil {
				out["usage"] = e.Compaction.Usage
			}
		}
		return out
	case agentcore.RunEnd:
		return map[string]any{"type": "run_end", "reason": e.Reason, "error": errText(e.Err), "turns": e.Turns, "tool_calls": e.ToolCalls, "failed_calls": e.FailedCalls}
	}
	panic(fmt.Sprintf("print: unknown event %T", ev))
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
