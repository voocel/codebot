package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/snapshot"
	"github.com/voocel/codebot/internal/storage"
	cbteam "github.com/voocel/codebot/internal/team"
	localtools "github.com/voocel/codebot/internal/tools"
)

func assembleRuntime(input *resolvedInput, services *bootServices, assembly *sessionAssembly) (*Runtime, error) {
	taskRT := task.NewRuntime()
	teamReg := team.NewRegistry()
	teammateEvents := agent.NewTeammateEventHub()
	sessionID := input.sessionStore.Header().SessionID
	// Pre-create a default team so the leader can spawn teammates immediately
	// (subagent { name: ... }) without a separate team_create step. The team
	// name is purely cosmetic until the model decides to rename via team_create
	// for a meaningful project label.
	if err := teamReg.CreateTeam(cbteam.DefaultTeamName, "", sessionID); err != nil {
		return nil, fmt.Errorf("pre-create default team: %w", err)
	}
	taskTools := localtools.NewTaskTools(services.taskStore, taskRT, assembly.hookRunner)
	teamTools := localtools.NewTeamTools(teamReg, taskRT, sessionID)
	// Drop a dismissed teammate from the persisted roster so resume doesn't
	// resurrect it. The dismiss tool is leader-only; find it in the team set.
	if services.rosterStore != nil {
		for _, t := range teamTools {
			if dt, ok := t.(*localtools.TeamDismissTool); ok {
				dt.SetRosterRemover(services.rosterStore.RemoveMember)
			}
		}
	}
	tools := make([]agentcore.Tool, 0, len(assembly.tools)+len(taskTools)+len(teamTools))
	tools = append(tools, assembly.tools...)
	tools = append(tools, taskTools...)
	tools = append(tools, teamTools...)
	baseTools := make([]agentcore.Tool, 0, len(assembly.baseTools)+len(taskTools)+len(teamTools))
	baseTools = append(baseTools, assembly.baseTools...)
	baseTools = append(baseTools, taskTools...)
	baseTools = append(baseTools, teamTools...)

	reserveTokens := 0 // 0 = engine default (fixed buffer)
	if r := assembly.settings.CompactRatio; r > 0 && r < 1 {
		reserveTokens = assembly.settings.ContextWindow - int(float64(assembly.settings.ContextWindow)*r)
	}
	contextEngine, summaryCompact := buildContextEngine(assembly.chatModel, assembly.settings.ContextWindow, reserveTokens, input.cwd)

	agentCore, err := buildAgent(assembly, services, contextEngine, tools)
	if err != nil {
		return nil, err
	}

	if err := restoreAgentState(input, assembly, tools, agentCore); err != nil {
		return nil, err
	}

	session := buildSession(input, services, assembly, contextEngine, agentCore, tools)
	// Bind the telemetry session-id provider now the session exists: every LLM
	// generation span (leader and teammates, same session) gets tagged so the
	// backend groups the run. Reads live, so a mid-run SwitchSession follows.
	if input.telemetryBindSession != nil {
		input.telemetryBindSession(session.SessionID)
	}
	wireSessionRuntime(input, assembly, services, session, baseTools, tools, agentCore, taskRT, contextEngine, summaryCompact)

	// Wire team spawn on the subagent tool. Each spawned teammate gets its
	// own send_message instance — a per-spawn instance keeps the tool
	// stateless from the registry's POV and avoids accidentally sharing a
	// captured ctx across teammates.
	//
	// baseBlocks reuses the leader's already-computed universal-base text
	// (assembly.frozenIdentity = BuildUniversalBase output). Sharing the
	// exact same bytes is the precondition for cross-agent prompt cache
	// reuse — Anthropic keys its cache on the byte string, so a teammate's
	// first request hits the same entry the leader's turns wrote.
	// SystemOverride sessions leave frozenIdentity empty; in that case we
	// pass nil so teammates degrade to their role block as the only system
	// content rather than carrying an empty cache-controlled block.
	if assembly.subagentTool != nil {
		var baseBlocks []agentcore.SystemBlock
		if assembly.frozenIdentity != "" {
			baseBlocks = []agentcore.SystemBlock{
				{Text: assembly.frozenIdentity, CacheControl: "ephemeral"},
			}
		}
		// Lazy snapshot: each spawn freezes the leader's CURRENT MCP / overlay
		// state into the teammate; later leader-side changes do not propagate.
		dynamicProvider := session.DynamicSystemBlock

		// Force-inject coordination tools so teammates can collaborate on the
		// shared task list. task_stop / team_* deliberately stay out — they
		// reshape coordination state and remain leader-only.
		extraTools := []agentcore.Tool{localtools.NewSendMessageTool(taskRT, teamReg)}
		for _, t := range localtools.NewTaskTools(services.taskStore, nil, assembly.hookRunner) {
			switch t.Name() {
			case "task_create", "task_update", "task_get", "task_list":
				extraTools = append(extraTools, t)
				if updater, ok := t.(*localtools.TaskUpdateTool); ok {
					updater.SetAssignmentNotifier(buildAssignmentNotifier(teamReg))
				}
			}
		}
		protocol := cbteam.Hooks(cbteam.HookOptions{
			IdleClaim:         buildIdleClaim(services.taskStore),
			IdleClaimInterval: 2 * time.Second,
		})
		spawner := agent.TeammateSpawner(
			teamReg,
			taskRT,
			extraTools,
			teammateEvents,
			baseBlocks,
			dynamicProvider,
			protocol,
			assembly.hookRunner,
			&agent.TeammatePersist{Roster: services.rosterStore, Transcripts: services.transcripts},
		)
		assembly.subagentTool.SetTeamSpawner(spawner)

		// Lazy teammate resume: boot does NO team work. Instead of eagerly
		// re-spawning prior teammates, each wakes on demand the first time the
		// leader messages it — the waker reuses the same spawner so a woken
		// teammate flows through identical tool injection / transcript recording
		// / roster upsert. A woken teammate joins the active (default) team; the
		// prior team's display name is not restored.
		if services.rosterStore != nil {
			waker := agent.NewTeammateWaker(spawner, assembly.subagentTool.AgentConfig, teamReg, services.rosterStore, services.transcripts)
			for _, t := range teamTools {
				if sm, ok := t.(*localtools.SendMessageTool); ok {
					sm.SetWaker(waker)
				}
			}
		}
	}

	pumpCtx, stopPump := context.WithCancel(context.Background())
	go agent.NewLeaderInboxPump(teamReg, agentCore, 0).Run(pumpCtx)

	modelName := config.FormatModelID(assembly.settings.Provider, assembly.settings.Model)
	if session != nil && session.ModelName() != "" {
		modelName = session.ModelName()
	}

	return &Runtime{
		Cwd:            input.cwd,
		GitBranch:      detectGitBranch(input.cwd),
		ApprovalEngine: services.approvalEngine,
		TaskRuntime:    taskRT,
		TeamRegistry:   teamReg,
		TeammateEvents: teammateEvents,
		Settings:       assembly.settings,
		ModelName:      modelName,
		Session:        session,
		SessionStore:   input.sessionStore,
		PluginCatalog:  services.pluginCatalog,
		SkillCatalog:   services.skillCatalog,
		MCPManager:     services.mcpManager,
		MCPServers:     services.mcpServers,
		HookRunner:     assembly.hookRunner,
		EnvHint:        input.envHint,
		PlanSlug:       input.sessionSnapshot.PlanSlug,
		PlanPhase:      input.sessionSnapshot.PlanPhase,
		PlanPreMode:    input.sessionSnapshot.PlanPreMode,
		stopTeamPump:   stopPump,
	}, nil
}

// buildIdleClaim is the work-stealing hook: pull the next claimable task on
// behalf of the calling teammate and return it as the synthetic prompt for
// the next turn. ok=false means no work / no identity / lost the CAS race.
func buildIdleClaim(store *storage.TaskStore) func(ctx context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) {
		if store == nil {
			return "", false
		}
		id := team.IdentityFromContext(ctx)
		if id == nil || id.AgentName == "" {
			return "", false
		}
		next := store.FindClaimable()
		if next == nil {
			return "", false
		}
		claimed, err := store.Claim(next.ID, id.AgentName)
		if err != nil {
			return "", false
		}
		return cbteam.FormatTaskClaimPrompt(claimed.ID, claimed.Subject, claimed.Description), true
	}
}

// buildAssignmentNotifier fires when a task's owner actually changes:
// resolves sender from ctx (leader → TeamLeadName), suppresses self-assign
// echo, and best-effort delivers an assignment notice to the new owner.
// Silent on missing / closed mailbox — the team was torn down mid-update.
func buildAssignmentNotifier(reg *team.Registry) localtools.AssignmentNotifier {
	return func(ctx context.Context, toAgent string, p localtools.AssignmentPayload) {
		if reg == nil || toAgent == "" {
			return
		}
		sender := team.TeamLeadName
		var color string
		if id := team.IdentityFromContext(ctx); id != nil {
			sender = id.AgentName
			color = id.Color
		}
		if sender == toAgent {
			return
		}
		mb := reg.Mailbox(toAgent)
		if mb == nil {
			return
		}
		text := fmt.Sprintf("You have been assigned task #%s: %s.", p.TaskID, p.Subject)
		if p.Description != "" {
			text += " " + p.Description
		}
		_ = mb.Send(team.Message{From: sender, Text: text, Color: color})
	}
}

func buildContextEngine(chatModel agentcore.ChatModel, contextWindow, reserveTokens int, cwd string) (*agentctx.ContextEngine, *agentctx.FullSummaryStrategy) {
	toolCompact := agentctx.NewToolResultMicrocompact(agentctx.ToolResultMicrocompactConfig{
		Classifier: agent.CodebotToolClassifier,
		KeepRecent: 5,
	})
	trimCompact := agentctx.NewLightTrim(agentctx.LightTrimConfig{})
	// SessionMemory-backed compaction runs ahead of the LLM summary path so
	// that a populated session-memory.md reuses the living document instead of
	// triggering a synchronous summarization call. Empty / template-only
	// memory files fall through to FullSummary automatically.
	memoryCompact := agentctx.NewSessionMemory(agentctx.SessionMemoryConfig{
		SeedFn: agent.SessionMemorySeedFn(cwd),
	})
	summaryCompact := agentctx.NewFullSummary(agentctx.FullSummaryConfig{
		Model: chatModel,
	})
	engine := agentctx.NewEngine(agentctx.EngineConfig{
		ContextWindow: contextWindow,
		ReserveTokens: reserveTokens,
		Strategies: []agentctx.Strategy{
			toolCompact,
			trimCompact,
			memoryCompact,
			summaryCompact,
		},
	})
	return engine, summaryCompact
}

func buildAgent(assembly *sessionAssembly, services *bootServices, contextEngine agentcore.ContextManager, tools []agentcore.Tool) (*agentcore.Agent, error) {
	opts := []agentcore.AgentOption{
		agentcore.WithModel(assembly.chatModel),
		agentcore.WithSystemBlocks(assembly.systemBlocks),
		agentcore.WithTools(tools...),
		agentcore.WithMaxTurns(assembly.settings.MaxTurns),
		agentcore.WithMaxRetries(5),
		agentcore.WithMaxToolErrors(3),
		agentcore.WithMaxToolConcurrency(4),
		agentcore.WithContextManager(contextEngine),
		agentcore.WithToolGate(services.approvalEngine.AsToolGate()),
		// Place the single message-level cache write breakpoint on the freshest
		// non-system turn (user input, tool_result, or assistant). System
		// blocks 1/2 already carry their own cache_control; this breakpoint
		// covers the growing prefix so each LLM call inside a tool loop reads
		// the previous tool_use+tool_result pair from cache instead of
		// re-uploading them.
		agentcore.WithCacheLastMessage("ephemeral"),
	}
	if assembly.hookMiddleware != nil {
		opts = append(opts, agentcore.WithMiddlewares(assembly.hookMiddleware))
	}
	return agentcore.NewAgent(opts...), nil
}

func restoreAgentState(input *resolvedInput, assembly *sessionAssembly, tools []agentcore.Tool, ag *agentcore.Agent) error {
	if len(input.sessionSnapshot.Messages) > 0 {
		if err := ag.SetMessages(input.sessionSnapshot.Messages); err != nil {
			return fmt.Errorf("restore agent messages: %w", err)
		}
		agentcore.ReactivateDeferred(tools, input.sessionSnapshot.Messages)
	}
	if input.sessionSnapshot.Thinking != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(input.sessionSnapshot.Thinking))
		assembly.settings.ThinkingLevel = input.sessionSnapshot.Thinking
	} else if assembly.settings.ThinkingLevel != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(assembly.settings.ThinkingLevel))
	}
	return nil
}

func buildSession(input *resolvedInput, services *bootServices, assembly *sessionAssembly, contextEngine agentcore.ContextManager, ag *agentcore.Agent, tools []agentcore.Tool) *agent.Session {
	return agent.NewSession(agent.SessionConfig{
		Agent:                 ag,
		ContextManager:        contextEngine,
		Store:                 input.sessionStore,
		Manager:               input.sessionManager,
		Registry:              input.registry,
		Settings:              assembly.settings,
		Cwd:                   input.cwd,
		CreateModel:           input.modelFactory,
		ChatModel:             assembly.chatModel,
		Tools:                 tools,
		TaskStore:             services.taskStore,
		ContextFiles:          assembly.contextFiles,
		Skills:                services.skills,
		SkillCatalog:          services.skillCatalog,
		SkillUsage:            services.skillUsage,
		HookRunner:            assembly.hookRunner,
		Snapshotter:           buildSnapshotter(input.cwd, assembly.settings.Snapshot),
		FrozenIdentity:        assembly.frozenIdentity,
		FrozenInstructions:    assembly.frozenInstructions,
		InitialMCPOverlay:     assembly.initialMCPOverlay,
		InitialDynamic:        assembly.initialDynamic,
		DeferredToolsPreamble: assembly.deferredToolsPreamble,
		Reminders:             assembly.reminders,
		PreambleInjected:      len(input.sessionSnapshot.Messages) > 0,
		SkillAllowsSetter:     services.approvalEngine.SetSkillAllows,
		FileReadState:         assembly.fileReadState,
	})
}

// buildSnapshotter returns a workspace snapshotter for /undo, or nil when it is
// disabled. Mirrors opencode's enabled(): off unless the setting is on AND cwd
// is a git repository (the shadow repo borrows git's plumbing, so /undo is
// git-only for now).
func buildSnapshotter(cwd string, enabled bool) agent.Snapshotter {
	if !enabled || !config.IsGitRepo(cwd) {
		return nil
	}
	return snapshot.New(config.SnapshotDir(cwd), cwd)
}

func wireSessionRuntime(input *resolvedInput, assembly *sessionAssembly, services *bootServices, session *agent.Session, baseTools, tools []agentcore.Tool, ag *agentcore.Agent, taskRT *task.Runtime, contextEngine *agentctx.ContextEngine, summaryCompact *agentctx.FullSummaryStrategy) {
	summaryCompact.SetPostSummaryHooks(session.PostSummaryRecoveryHook())
	contextEngine.SetProjectHook(session.HandleProjectedRewrite)
	contextEngine.SetRecoverHook(session.HandleOverflowRewrite)

	for _, tool := range tools {
		if st, ok := tool.(*localtools.SkillTool); ok {
			st.SetInvocationApplier(session.ApplySkillInvocation)
		}
	}

	assembly.subagentTool.SetTaskRuntime(taskRT)
	assembly.subagentTool.SetNotifyFn(ag.FollowUp)

	sessionID := input.sessionStore.Header().SessionID
	bgDir := filepath.Join(config.SessionsDir(input.cwd), sessionID, "bg")
	assembly.subagentTool.SetBgOutputFactory(func(taskID, agentName string) (io.WriteCloser, string, error) {
		dir := filepath.Join(bgDir, taskID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", err
		}
		path := filepath.Join(dir, "output.jsonl")
		f, err := os.Create(path)
		if err != nil {
			return nil, "", err
		}
		meta, _ := json.Marshal(map[string]string{"agent": agentName})
		_ = os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o600)
		return f, path, nil
	})

	if assembly.bashTool != nil {
		assembly.bashTool.SetTaskRuntime(taskRT)
		assembly.bashTool.SetNotifyFn(ag.FollowUp)
		assembly.bashTool.SetBgOutputFactory(func(shellID string) (io.WriteCloser, string, error) {
			dir := filepath.Join(bgDir, shellID)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, "", err
			}
			path := filepath.Join(dir, "output.log")
			f, err := os.Create(path)
			return f, path, err
		})
	}

	if services.mcpManager != nil {
		session.SetBeforePrompt(func() {
			mcpTools, ok := services.mcpManager.RefreshIfDirty(context.Background())
			if !ok {
				return
			}
			all := make([]agentcore.Tool, len(baseTools), len(baseTools)+len(mcpTools))
			copy(all, baseTools)
			all = append(all, mcpTools...)
			session.ReplaceAllTools(all)
		})
	}

	if assembly.hookRunner != nil {
		assembly.hookRunner.RunSessionStart(context.Background())
	}
}

func detectGitBranch(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
