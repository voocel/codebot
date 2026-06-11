package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
	"github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

// wireLifecycle assembles the frontend-neutral slice of the session lifecycle:
// plan/goal managers (created and restored from the session snapshot), goal
// tool callbacks, and the session-scoped cron store. Frontends contribute only
// interaction callbacks on top — approval prompts, ask_user, display
// notifications, event rendering.
func wireLifecycle(rt *Runtime, input *resolvedInput) error {
	planStore := storage.NewPlanStore(config.PlansDir(input.cwd))
	planManager := plan.NewManager(rt.Session, rt.ApprovalEngine, planStore, input.sessionStore)
	_ = planManager.Restore(plan.State{
		Phase:   plan.Phase(input.sessionSnapshot.PlanPhase),
		Slug:    input.sessionSnapshot.PlanSlug,
		PreMode: input.sessionSnapshot.PlanPreMode,
	})

	goalManager := goal.NewManager(rt.Session, rt.Session)
	goalManager.SetSuspender(func() bool {
		return planManager.Snapshot().Phase != plan.PhaseOff
	})
	if err := goalManager.Restore(goal.StateFromEntry(input.sessionSnapshot.Goal)); err != nil {
		return fmt.Errorf("restore goal state: %w", err)
	}
	WireGoalTools(rt.Session, goalManager)

	rt.PlanStore = planStore
	rt.PlanManager = planManager
	rt.GoalManager = goalManager
	rt.CronStore = resolveCronStore(rt.Session, input.cwd)
	return nil
}

// WireGoalTools connects goal tool callbacks and the usage-limit handler to
// the goal manager. Called at boot and again by the UI after a plugin reload
// rebuilds the toolset.
func WireGoalTools(sess *agent.Session, mgr *goal.Manager) {
	if sess == nil || mgr == nil {
		return
	}
	sess.SetGoalUsageLimitHandler(func(reason string) (goal.State, error) {
		state := mgr.Snapshot().Normalize()
		if state.Status != goal.StatusActive && state.Status != goal.StatusBudgetLimited {
			return state, nil
		}
		return mgr.UsageLimit(reason)
	})
	for _, tool := range sess.ToolsByName("create_goal", "get_goal", "update_goal") {
		switch t := tool.(type) {
		case *localtools.GoalCreateTool:
			t.SetCreator(mgr.CreateWithBudget)
		case *localtools.GoalGetTool:
			t.SetSnapshotter(mgr.Snapshot)
		case *localtools.GoalUpdateTool:
			t.SetHandlers(mgr.Complete, mgr.Block)
		}
	}
}

// resolveCronStore locates the store behind the cron_create tool and scopes
// its durable file to the current session directory.
func resolveCronStore(sess *agent.Session, cwd string) *cron.Store {
	found := sess.ToolsByName("cron_create")
	if len(found) == 0 {
		return nil
	}
	ct, ok := found[0].(*localtools.CronCreateTool)
	if !ok {
		return nil
	}
	store := ct.Store()
	if info, err := sess.CurrentSessionInfo(); err == nil && info.ID != "" {
		store.SetConfigDir(filepath.Join(config.SessionsDir(cwd), info.ID))
	}
	return store
}

// MCPReport summarizes a ConnectMCP run for frontend display.
type MCPReport struct {
	Tools  int
	Errors []string
}

// ConnectMCP connects all configured MCP servers and warms the tool list.
// Blocking — async frontends run it in a goroutine. Returns nil when no
// servers are configured.
//
// It deliberately never touches the session: session prompt state is not
// goroutine-safe, so MCP tools and instructions enter the session through
// exactly one writer — the before-prompt refresh hook, which MarkDirty arms
// and which runs on the prompt path ahead of the turn.
func (r *Runtime) ConnectMCP(ctx context.Context) *MCPReport {
	if r.MCPManager == nil || len(r.MCPServers) == 0 {
		return nil
	}
	errs := r.MCPManager.StartAll(ctx, r.MCPServers)
	tools := r.MCPManager.Tools(ctx)
	r.MCPManager.MarkDirty()
	report := &MCPReport{Tools: len(tools)}
	for _, e := range errs {
		report.Errors = append(report.Errors, e.Error())
	}
	return report
}

// StartCron starts the session cron scheduler. Fired prompts are delivered
// through onFire — the frontend decides how they enter the conversation (the
// TUI injects them as user prompts). Returns a stop function, or nil when no
// cron store is available. Print mode never starts cron: a one-shot run has
// no loop to schedule against.
func (r *Runtime) StartCron(onFire func(prompt string)) func() {
	if r.CronStore == nil || r.Session == nil || onFire == nil {
		return nil
	}
	var sessionID string
	if info, err := r.Session.CurrentSessionInfo(); err == nil {
		sessionID = info.ID
	}
	sched := cron.NewScheduler(cron.SchedulerConfig{
		Store:          r.CronStore,
		SessionID:      sessionID,
		RestoreDurable: len(r.Session.Messages()) > 0,
		OnFire:         onFire,
		IsBusy:         r.Session.IsRunning,
	})
	sched.Start()
	return sched.Stop
}
