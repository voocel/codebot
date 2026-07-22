package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/bootstrap"
	"github.com/voocel/codebot/internal/config"
	goalstate "github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/ui/commands"
	"github.com/voocel/codebot/internal/ui/tui"
)

// RunOnboarding runs the first-run setup wizard. Called by main() before the
// runtime boots when no configuration exists (or with -setup).
func RunOnboarding() (tui.OnboardingResult, error) {
	return tui.RunOnboarding()
}

// RunTUI executes interactive TUI mode. The frontend-neutral session
// lifecycle (plan/goal managers, goal tool callbacks, MCP manager, cron
// store) is already assembled by bootstrap; this function only adds the
// TUI-specific bindings on top.
func RunTUI(rt *bootstrap.Runtime, version string) error {
	sess := rt.Session
	cwd := rt.Cwd
	approvalEngine := rt.ApprovalEngine

	adapter := &App{
		Session:        sess,
		Cwd:            cwd,
		GitBranch:      rt.GitBranch,
		Version:        version,
		ApprovalEngine: approvalEngine,
		TaskRuntime:    rt.TaskRuntime,
		TeamRegistry:   rt.TeamRegistry,
		TeammateEvents: rt.TeammateEvents,
		Commands:       nil,
		Skills:         sess.Skills(),
		PluginCatalog:  rt.PluginCatalog,
		SkillCatalog:   rt.SkillCatalog,
		PlanStore:      rt.PlanStore,
		SessionStore:   rt.SessionStore,
		PlanManager:    rt.PlanManager,
		GoalManager:    rt.GoalManager,
		MCPManager:     rt.MCPManager,
		MCPServers:     rt.MCPServers,
		CronStore:      rt.CronStore,
		Dreamer:        rt.Dreamer,
		History:        newInputHistory(sess, cwd),
	}
	adapter.Commands = adapter.loadPluginCommands()
	adapter.wireWorktree(rt)

	adapter.rebuildRegistry()
	cfg := adapter.Config()
	cfg.Version = version
	cfg.Provider = sess.Provider()
	// "" means provider default: reasoning models are effectively thinking on
	// auto, so surface that on the welcome card (same wording as /model).
	cfg.ReasoningEffort = sess.Settings().ReasoningEffort
	if cfg.ReasoningEffort == "" {
		for _, lvl := range sess.AvailableThinkingLevelsFor(sess.Provider(), sess.ModelName()) {
			if lvl != "" && lvl != "off" {
				cfg.ReasoningEffort = "auto"
				break
			}
		}
	}
	cfg.ContextWindow = rt.Settings.ContextWindow
	cfg.RestoredMessages = sess.Messages()
	if snap := sess.TaskSnapshot(); snap.Total > 0 {
		cfg.InitialTasks = &snap
	}
	cfg.OnHideCompletedTasks = func(_ storage.TaskSnapshot) tea.Cmd {
		return func() tea.Msg {
			if err := sess.ResetTaskList(); err != nil {
				return tui.CommandResultMsg{
					Text: tui.ErrorStyle.Render("Task list reset failed: " + err.Error()),
				}
			}
			return nil
		}
	}
	m := tui.New(sess, rt.ModelName, cfg)
	m.MCPLoading = len(rt.MCPServers) > 0
	p := tea.NewProgram(m)
	sendAsync := func(msg tea.Msg) {
		go p.Send(msg)
	}

	// Connect MCP servers in background; the TUI shows a spinner until ready.
	if m.MCPLoading {
		go func() {
			if report := rt.ConnectMCP(context.Background()); report != nil {
				p.Send(tui.MCPReadyMsg{Tools: report.Tools, Errors: report.Errors})
			}
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
						return nil, context.Canceled
					}
					return resp, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
		}
	}

	// Wire Task tools to TUI — notify on every task mutation.
	sess.SetTaskNotifyFn(func(snap storage.TaskSnapshot) {
		p.Send(tui.TaskListUpdateMsg{Snapshot: snap})
	})

	// Start the cron scheduler; fired prompts enter the conversation as user input.
	if stop := rt.StartCron(func(prompt string) { p.Send(tui.PromptMsg{Text: prompt}) }); stop != nil {
		defer stop()
	}

	// Wire runtime approval prompts to TUI.
	if approvalEngine != nil {
		approvalEngine.SetApprover(func(ctx context.Context, prompt approval.Prompt) (approval.Choice, error) {
			respCh := make(chan tui.PermitChoice, 1)
			warning := ""
			if prompt.Tool == "bash" {
				warning = approval.DestructiveCommandWarning(prompt.Summary)
			}
			p.Send(tui.PermissionMsg{
				Tool:         prompt.Tool,
				Command:      prompt.Summary,
				Reason:       prompt.Reason,
				Preview:      prompt.Preview,
				Warning:      warning,
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
				sendAsync(tui.CommandResultMsg{Text: tui.MutedStyle.Render(text)})
			} else {
				sendAsync(tui.CommandResultMsg{Text: tui.CommandStyle.Render(text)})
			}
			return
		}
		if prefix, delay, ok := formatRetryEvent(ev); ok {
			p.Send(tui.RetryStatusMsg{
				Prefix:   prefix,
				Deadline: time.Now().Add(delay),
			})
			return
		}
		if ev.Type == agent.SEAutoRetryEnd {
			p.Send(tui.RetryStatusMsg{})
			return
		}
		if ev.Type == agent.SERuntimeReminder && ev.Reminder != "" {
			sendAsync(tui.CommandResultMsg{
				Text: tui.MutedStyle.Render("Runtime reminder triggered: " + formatRuntimeReminderKind(ev.ReminderKind) + "."),
			})
			return
		}
		if text, ok := formatGoalEvent(ev); ok {
			sendAsync(tui.CommandResultMsg{Text: tui.CommandStyle.Render(text)})
			return
		}
		if ev.Type == agent.SEError && ev.Error != nil {
			p.Send(tui.RetryStatusMsg{})
			sendAsync(tui.CommandResultMsg{
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

// formatRetryEvent extracts the static prefix and remaining delay from a
// retry-start event. Returned values feed RetryStatusMsg; the TUI renders
// the live countdown from Deadline = now + delay.
func formatRetryEvent(ev agent.SessionEvent) (prefix string, delay time.Duration, ok bool) {
	if ev.Type != agent.SEAutoRetryStart {
		return "", 0, false
	}
	if ev.RetryMax > 0 {
		return fmt.Sprintf("Request failed, retrying (%d/%d)", ev.RetryAttempt, ev.RetryMax), ev.RetryDelay, true
	}
	return "Request failed, retrying", ev.RetryDelay, true
}

func formatAutoCompactionEvent(ev agent.SessionEvent) (text string, muted bool, ok bool) {
	if ev.CompactionReason == "manual" {
		return "", false, false
	}
	strategySuffix := ""
	if label := commands.PrettyCompactionStrategy(ev.CompactionStrategy); label != "" {
		strategySuffix = " via " + label
	}

	switch ev.Type {
	case agent.SEAutoCompactionStart:
		switch ev.CompactionReason {
		case "overflow":
			return "Context overflow detected; compacting automatically" + strategySuffix + "...", true, true
		default:
			return "Auto-compacting context" + strategySuffix + "...", true, true
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
					"Context %s after overflow%s: %s -> %s.",
					action,
					strategySuffix,
					tui.FormatTokens(ev.TokensBefore),
					tui.FormatTokens(ev.TokensAfter),
				), false, true
			default:
				return fmt.Sprintf(
					"Context %s automatically%s: %s -> %s.",
					action,
					strategySuffix,
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

func formatGoalEvent(ev agent.SessionEvent) (string, bool) {
	switch ev.Type {
	case agent.SEGoalCleared:
		return "Goal cleared.", true
	case agent.SEGoalUpdated:
		previous := ev.GoalPrevious.Normalize()
		current := ev.Goal.Normalize()
		if current.Status == previous.Status &&
			current.BlockedCount == previous.BlockedCount &&
			current.BlockedReason == previous.BlockedReason &&
			current.TokenBudget == previous.TokenBudget {
			return "", false
		}
		switch current.Status {
		case goalstate.StatusActive:
			if current.BlockedReason != "" && current.BlockedCount != previous.BlockedCount {
				return fmt.Sprintf("Goal blocked audit: %d/3.", current.BlockedCount), true
			}
			if previous.Status == goalstate.StatusPaused || previous.Status == goalstate.StatusBlocked || previous.Status == goalstate.StatusBudgetLimited || previous.Status == goalstate.StatusUsageLimited {
				return "Goal resumed: " + current.Objective, true
			}
			if previous.Status == goalstate.StatusOff {
				return "Goal set: " + current.Objective, true
			}
			return "", false
		case goalstate.StatusPaused:
			return "Goal paused: " + current.Objective, true
		case goalstate.StatusComplete:
			return "Goal completed: " + current.Objective, true
		case goalstate.StatusBlocked:
			return "Goal blocked: " + current.Objective, true
		case goalstate.StatusBudgetLimited:
			return "Goal token budget reached: " + current.Objective, true
		case goalstate.StatusUsageLimited:
			return "Goal usage limited: " + current.Objective, true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

func formatRuntimeReminderKind(kind agent.RuntimeReminderKind) string {
	switch kind {
	case agent.ReminderRepeatToolCall:
		return "repeated tool call"
	case agent.ReminderPostStopValidation:
		return "post-stop validation failed"
	case agent.ReminderTaskManagement:
		return "task management reminder"
	case agent.ReminderPlanMode:
		return "plan mode reminder"
	default:
		if kind == "" {
			return "unknown"
		}
		return string(kind)
	}
}
