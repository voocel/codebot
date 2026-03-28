package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
)

// RunPrintMode runs the agent in non-interactive mode.
// stdout receives assistant text (pipe-friendly), stderr receives tool/status info.
// When jsonMode is true, all events are streamed as JSONL to stdout.
func RunPrintMode(sess *agent.Session, prompt string, jsonMode bool) error {
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }
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

	unsub := sess.Subscribe(func(ev agent.SessionEvent) {
		if text, _, ok := formatAutoCompactionEvent(ev); ok {
			if !jsonMode {
				fmt.Fprintln(os.Stderr, text)
			}
			return
		}
		if text, ok := formatRetryEvent(ev); ok {
			if !jsonMode {
				fmt.Fprintln(os.Stderr, text)
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
			fmt.Fprintf(os.Stderr, "[tool] %s\n", ae.Tool)

		case agentcore.EventToolExecEnd:
			if ae.IsError {
				fmt.Fprintf(os.Stderr, "[tool] %s error\n", ae.Tool)
			}

		case agentcore.EventError:
			if ae.Err != nil && !errors.Is(ae.Err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "error: %v\n", ae.Err)
				setExitErr(ae.Err)
			}
			closeDone()

		case agentcore.EventAgentEnd:
			closeDone()
		}
	})
	defer unsub()

	if err := sess.Prompt(prompt); err != nil {
		return err
	}

	<-done

	// Final newline for text mode.
	if !jsonMode {
		fmt.Fprintln(os.Stdout)
	}

	return getExitErr()
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
