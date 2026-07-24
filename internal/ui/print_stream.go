package ui

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
	"github.com/voocel/agentcore/task"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/bootstrap"
	"github.com/voocel/codebot/internal/diag"
	"github.com/voocel/codebot/internal/ui/tui"
)

// RunPrint executes non-interactive print/json mode.
func RunPrint(rt *bootstrap.Runtime, args []string, jsonMode bool) error {
	prompt := strings.Join(args, " ")
	if prompt == "" {
		stdinPrompt, err := ReadStdinPrompt()
		if err != nil {
			return fmt.Errorf("stdin error: %w: %w", diag.ErrToolInput, err)
		}
		prompt = strings.TrimSpace(stdinPrompt)
	}
	if prompt == "" {
		return fmt.Errorf("print mode requires a prompt (argument or stdin pipe): %w", diag.ErrToolInput)
	}

	// Connect MCP servers before the one-shot prompt runs — unlike the TUI
	// there is no later turn to pick them up, so this blocks (bounded) and
	// connection failures degrade to a stderr note.
	if len(rt.MCPServers) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		report := rt.ConnectMCP(ctx)
		cancel()
		if report != nil {
			for _, e := range report.Errors {
				fmt.Fprintf(os.Stderr, "mcp: %s\n", e)
			}
		}
	}

	if err := RunPrintMode(rt.Session, rt.TaskRuntime, prompt, jsonMode); err != nil {
		return fmt.Errorf("print mode: %w", err)
	}
	return nil
}

// RunPrintMode runs the agent in non-interactive mode.
// stdout receives assistant text (pipe-friendly), stderr receives tool/status info.
// When jsonMode is true, all events are streamed as JSONL to stdout.
func RunPrintMode(sess *agent.Session, taskRT *task.Runtime, prompt string, jsonMode bool) error {
	var errMu sync.Mutex
	var exitErr error
	setExitErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		defer errMu.Unlock()
		if exitErr == nil {
			exitErr = err
		}
	}
	getExitErr := func() error {
		errMu.Lock()
		defer errMu.Unlock()
		return exitErr
	}
	hiddenToolCalls := make(map[string]struct{})

	unsub := sess.Subscribe(func(ev agent.SessionEvent) {
		if text, _, ok := formatAutoCompactionEvent(ev); ok {
			if !jsonMode {
				fmt.Fprintln(os.Stderr, text)
			}
			return
		}
		if prefix, delay, ok := formatRetryEvent(ev); ok {
			if !jsonMode {
				fmt.Fprintf(os.Stderr, "%s in %s...\n", prefix, delay.Truncate(time.Millisecond))
			}
			return
		}
		if ev.Type == agent.SEError && ev.Error != nil {
			fmt.Fprintf(os.Stderr, "session error: %v\n", ev.Error)
			setExitErr(ev.Error)
			return
		}
		if ev.Type == agent.SERuntimeReminder && ev.Reminder != "" {
			if !jsonMode {
				fmt.Fprintf(os.Stderr, "runtime reminder triggered: %s\n", formatRuntimeReminderKind(ev.ReminderKind))
			}
			return
		}
		if ev.Type != agent.SEAgentEvent || ev.AgentEvent == nil {
			return
		}
		ae := ev.AgentEvent

		if jsonMode {
			data, _ := json.Marshal(ae)
			fmt.Fprintln(os.Stdout, string(data))
			return
		}

		// Text mode: stream deltas to stdout, tool info to stderr.
		switch ae.Type {
		case agentcore.EventMessageUpdate:
			if ae.Delta != "" && ae.Message != nil && ae.Message.GetRole() == agentcore.RoleAssistant {
				fmt.Fprint(os.Stdout, ae.Delta)
			}

		case agentcore.EventToolExecStart:
			// Mirror TUI: hide internal tool calls from text mode stderr too.
			// JSON mode above stays unfiltered so scripts that consume the
			// JSONL stream still get full audit data.
			if tui.IsHiddenToolCall(ae.Tool, ae.Args) {
				hiddenToolCalls[ae.ToolID] = struct{}{}
				return
			}
			fmt.Fprintf(os.Stderr, "[tool] %s\n", ae.Tool)

		case agentcore.EventToolExecEnd:
			if _, hidden := hiddenToolCalls[ae.ToolID]; hidden || tui.IsHiddenToolCall(ae.Tool, ae.Args) {
				delete(hiddenToolCalls, ae.ToolID)
				return
			}
			if ae.IsError {
				fmt.Fprintf(os.Stderr, "[tool] %s error\n", ae.Tool)
			}

		case agentcore.EventError:
			if ae.Err != nil && !errors.Is(ae.Err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "error: %v\n", ae.Err)
				setExitErr(ae.Err)
			}
		}
	})
	defer unsub()

	if err := sess.Prompt(prompt); err != nil {
		return err
	}

	waitForPrintCompletion(sess, taskRT)

	// Final newline for text mode.
	if !jsonMode {
		fmt.Fprintln(os.Stdout)
	}

	return getExitErr()
}

func waitForPrintCompletion(sess *agent.Session, taskRT *task.Runtime) {
	for {
		sess.WaitForIdle()
		if taskRT == nil {
			return
		}
		taskRT.Wait()
		if !sess.IsRunning() && taskRT.Active() == 0 {
			return
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
	reader := bufio.NewReader(os.Stdin)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}
