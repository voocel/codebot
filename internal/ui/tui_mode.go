package ui

import (
	"context"
	"fmt"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/ui/tui"
)

// RunTUI executes interactive TUI mode.
func RunTUI(sess *agent.Session, cwd, gitBranch, modelName string, profile policy.Profile, mcpMgr *mcpclient.Manager) error {
	adapter := &App{
		Session:       sess,
		Cwd:           cwd,
		GitBranch:     gitBranch,
		PolicyProfile: profile,
		Templates:     config.LoadPromptTemplates(cwd),
		Skills:        sess.Skills(),
		PlanStore:     storage.NewPlanStore(config.PlansDir(cwd)),
		MCPManager:    mcpMgr,
		History:       newInputHistory(sess, cwd),
	}

	adapter.registry = adapter.initRegistry()
	m := tui.New(sess, modelName, adapter.Config())
	p := tea.NewProgram(m)

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

	unsub := sess.Subscribe(func(ev agent.SessionEvent) {
		if ev.Type == agent.SEAgentEvent && ev.AgentEvent != nil {
			p.Send(tui.AgentEventMsg{Event: *ev.AgentEvent})
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
