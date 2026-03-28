package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/ui/tui"
)

// RunTUI executes interactive TUI mode.
func RunTUI(sess *agent.Session, cwd, gitBranch, modelName, version string, approvalEngine *approval.Engine, mcpMgr *mcpclient.Manager, mcpServers map[string]mcpclient.ServerConfig, envHint string) error {
	adapter := &App{
		Session:        sess,
		Cwd:            cwd,
		GitBranch:      gitBranch,
		ApprovalEngine: approvalEngine,
		Commands:       config.LoadFileCommands(cwd),
		Skills:         sess.Skills(),
		PlanStore:      storage.NewPlanStore(config.PlansDir(cwd)),
		MCPManager:     mcpMgr,
		History:        newInputHistory(sess, cwd),
	}

	adapter.rebuildRegistry()
	cfg := adapter.Config()
	cfg.Version = version
	cfg.EnvHint = envHint
	cfg.RestoredMessages = sess.Messages()
	cfg.OnMCPReady = func(msg tui.MCPReadyMsg) {
		if msg.Instructions != "" {
			sess.SetSystemSuffix(msg.Instructions)
		}
	}
	m := tui.New(sess, modelName, cfg)
	m.MCPLoading = mcpMgr != nil && len(mcpServers) > 0
	p := tea.NewProgram(m)

	// Start MCP server connections in background.
	if mcpMgr != nil && len(mcpServers) > 0 {
		go func() {
			errs := mcpMgr.StartAll(context.Background(), mcpServers)
			// Fetch tools eagerly so the count is accurate and servers are warmed up.
			tools := mcpMgr.Tools(context.Background())
			mcpMgr.MarkDirty()

			var instructions string
			if insts := mcpMgr.Instructions(); len(insts) > 0 {
				instructions = strings.Join(insts, "\n\n")
			}
			var errStrs []string
			for _, e := range errs {
				errStrs = append(errStrs, e.Error())
			}
			p.Send(tui.MCPReadyMsg{Tools: len(tools), Errors: errStrs, Instructions: instructions})
		}()
	}

	// Wire AskUserQuestion tool to TUI (find from session's registered tools).
	if found := sess.ToolsByName("ask_user"); len(found) > 0 {
		if askTool, ok := found[0].(*tools.AskUserTool); ok {
			askTool.SetHandler(func(ctx context.Context, questions []tools.Question) (*tools.AskUserResponse, error) {
				respCh := make(chan *tools.AskUserResponse, 1)
				p.Send(tui.AskUserMsg{Questions: questions, RespCh: respCh})
				select {
				case resp, ok := <-respCh:
					if !ok || resp == nil {
						return nil, fmt.Errorf("user cancelled")
					}
					return resp, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
		}
	}

	// Wire Task tools to TUI — notify on every task mutation.
	if found := sess.ToolsByName("task_create"); len(found) > 0 {
		if ct, ok := found[0].(*tools.TaskCreateTool); ok {
			ct.SetNotifyFn(func(snap tools.TaskSnapshot) {
				p.Send(tui.TaskListUpdateMsg{Snapshot: snap})
			})
			// Send initial snapshot for resumed sessions with persisted tasks.
			if snap := ct.Store().Snapshot(); snap.Total > 0 {
				p.Send(tui.TaskListUpdateMsg{Snapshot: snap})
			}
		}
	}

	// Wire Cron tools to TUI — start scheduler that sends prompts.
	if found := sess.ToolsByName("cron_create"); len(found) > 0 {
		if ct, ok := found[0].(*tools.CronCreateTool); ok {
			store := ct.Store()
			adapter.CronStore = store

			var sessionID string
			if info, err := sess.CurrentSessionInfo(); err == nil {
				sessionID = info.ID
			}
			if sessionID != "" {
				store.SetConfigDir(filepath.Join(config.SessionsDir(cwd), sessionID))
			}

			sched := cron.NewScheduler(cron.SchedulerConfig{
				Store:          store,
				SessionID:      sessionID,
				RestoreDurable: len(sess.Messages()) > 0,
				OnFire:         func(prompt string) { p.Send(tui.PromptMsg{Text: prompt}) },
				IsBusy:         sess.IsRunning,
			})
			sched.Start()
			defer sched.Stop()
		}
	}

	// Wire SubAgentTool and BashTool for /tasks command.
	if found := sess.ToolsByName("subagent"); len(found) > 0 {
		if st, ok := found[0].(*agentcore.SubAgentTool); ok {
			adapter.SubAgentTool = st
		}
	}
	if found := sess.ToolsByName("bash"); len(found) > 0 {
		tool := found[0]
		// Unwrap OutputLimitedTool wrapper if present.
		if w, ok := tool.(interface{ Unwrap() agentcore.Tool }); ok {
			tool = w.Unwrap()
		}
		if bt, ok := tool.(*agentcoretools.BashTool); ok {
			adapter.BashTool = bt
		}
	}

	// Wire runtime approval prompts to TUI.
	if approvalEngine != nil {
		approvalEngine.SetApprover(func(ctx context.Context, prompt approval.Prompt) (approval.Choice, error) {
			respCh := make(chan tui.PermitChoice, 1)
			p.Send(tui.PermissionMsg{
				Tool:         prompt.Tool,
				Command:      prompt.Summary,
				Reason:       prompt.Reason,
				Preview:      prompt.Preview,
				OutsideRoots: prompt.OutsideRoots,
				RespCh:       respCh,
			})
			select {
			case choice := <-respCh:
				switch choice {
				case tui.PermitChoiceAllowOnce:
					return approval.ChoiceAllowOnce, nil
				case tui.PermitChoiceAllowSession:
					return approval.ChoiceAllowSession, nil
				case tui.PermitChoiceAllowAlways:
					return approval.ChoiceAllowAlways, nil
				default:
					return approval.ChoiceDeny, nil
				}
			case <-ctx.Done():
				p.Send(tui.PermissionDismissMsg{})
				return approval.ChoiceDeny, ctx.Err()
			}
		})
	}

	unsub := sess.Subscribe(func(ev agent.SessionEvent) {
		if ev.Type == agent.SEAgentEvent && ev.AgentEvent != nil {
			p.Send(tui.AgentEventMsg{Event: *ev.AgentEvent})

			// Generate prompt suggestion after agent completes a turn.
			if ev.AgentEvent.Type == agentcore.EventAgentEnd {
				go func() {
					defer func() { recover() }()
					if approvalEngine != nil && approvalEngine.PlanMode() {
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if suggestion, err := sess.GenerateSuggestion(ctx); err == nil && suggestion != "" {
						p.Send(tui.SuggestionMsg{Text: suggestion})
					}
				}()
			}
			return
		}
		if text, muted, ok := formatAutoCompactionEvent(ev); ok {
			if muted {
				p.Send(tui.CommandResultMsg{Text: tui.MutedStyle.Render(text)})
			} else {
				p.Send(tui.CommandResultMsg{Text: tui.CommandStyle.Render(text)})
			}
			return
		}
		if text, ok := formatRetryEvent(ev); ok {
			p.Send(tui.CommandResultMsg{Text: tui.MutedStyle.Render(text)})
			return
		}
		if ev.Type == agent.SERuntimeReminder && ev.Reminder != "" {
			p.Send(tui.CommandResultMsg{
				Text: tui.MutedStyle.Render("Runtime reminder triggered: " + formatRuntimeReminderKind(ev.ReminderKind) + "."),
			})
			return
		}
		if ev.Type == agent.SEError && ev.Error != nil {
			p.Send(tui.CommandResultMsg{
				Text: tui.ErrorStyle.Render("Session error: " + ev.Error.Error()),
			})
		}
	})
	defer unsub()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	return nil
}

// newInputHistory creates a History scoped to the current session and project.
func newInputHistory(sess *agent.Session, cwd string) *storage.History {
	var sessionID string
	if info, err := sess.CurrentSessionInfo(); err == nil {
		sessionID = info.ID
	}
	return storage.NewHistory(
		filepath.Join(config.UserConfigDir(), "history.jsonl"),
		cwd,
		sessionID,
	)
}

func formatRetryEvent(ev agent.SessionEvent) (text string, ok bool) {
	switch ev.Type {
	case agent.SEAutoRetryStart:
		if ev.RetryMax > 0 {
			return fmt.Sprintf("Request failed, retrying (%d/%d) in %s...",
				ev.RetryAttempt, ev.RetryMax, ev.RetryDelay.Truncate(time.Millisecond)), true
		}
		return fmt.Sprintf("Request failed, retrying in %s...",
			ev.RetryDelay.Truncate(time.Millisecond)), true
	default:
		return "", false
	}
}

func formatAutoCompactionEvent(ev agent.SessionEvent) (text string, muted bool, ok bool) {
	if ev.CompactionReason == "manual" {
		return "", false, false
	}

	switch ev.Type {
	case agent.SEAutoCompactionStart:
		switch ev.CompactionReason {
		case "overflow":
			return "Context overflow detected; compacting automatically...", true, true
		default:
			return "Auto-compacting context...", true, true
		}
	case agent.SEAutoCompactionEnd:
		if ev.CompactionChanged && ev.TokensBefore > 0 && ev.TokensAfter > 0 {
			action := "compacted"
			switch ev.CompactionKind {
			case agent.CompactionKindTrim:
				action = "trimmed"
			case agent.CompactionKindPrune:
				action = "pruned"
			}
			switch ev.CompactionReason {
			case "overflow":
				return fmt.Sprintf(
					"Context %s after overflow: %s -> %s.",
					action,
					tui.FormatTokens(ev.TokensBefore),
					tui.FormatTokens(ev.TokensAfter),
				), false, true
			default:
				return fmt.Sprintf(
					"Context %s automatically: %s -> %s.",
					action,
					tui.FormatTokens(ev.TokensBefore),
					tui.FormatTokens(ev.TokensAfter),
				), false, true
			}
		}
		return "Auto compaction finished; context unchanged.", true, true
	default:
		return "", false, false
	}
}

func formatRuntimeReminderKind(kind agent.RuntimeReminderKind) string {
	switch kind {
	case agent.ReminderRepeatToolCall:
		return "repeated tool call"
	case agent.ReminderUnfinishedTasks:
		return "unfinished tasks"
	default:
		if kind == "" {
			return "unknown"
		}
		return string(kind)
	}
}
