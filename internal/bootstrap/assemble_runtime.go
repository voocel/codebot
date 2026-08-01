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
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/dream"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/snapshot"
	"github.com/voocel/codebot/internal/storage"
	cbteam "github.com/voocel/codebot/internal/team"
	localtools "github.com/voocel/codebot/internal/tools"
)

func assembleRuntime(input *resolvedInput, services *bootServices, assembly *sessionAssembly) (*Runtime, error) {
	taskRT := task.NewRuntime()
	teamReg := team.NewRegistry()
	teammateEvents := cbteam.NewEventHub()
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

	reserveTokens := 0 // 0 = engine default (fixed buffer)
	if r := assembly.settings.CompactRatio; r > 0 && r < 1 {
		reserveTokens = assembly.settings.ContextWindow - int(float64(assembly.settings.ContextWindow)*r)
	}
	contextEngine, summaryCompact, toolCompact := buildContextEngine(assembly.chatModel, assembly.settings.ContextWindow, reserveTokens, input.cwd)

	agentCore, err := buildAgent(assembly, services, contextEngine, tools, sessionID)
	if err != nil {
		return nil, err
	}

	if err := restoreAgentState(input, assembly, tools, agentCore); err != nil {
		return nil, err
	}

	session := buildSession(input, services, assembly, contextEngine, toolCompact, agentCore, tools)
	// Bind the telemetry session-id provider now the session exists: every LLM
	// generation span (leader and teammates, same session) gets tagged so the
	// backend groups the run. Reads live, so a mid-run SwitchSession follows.
	if input.telemetryTracer != nil {
		input.telemetryTracer.BindSession(session.SessionID)
	}
	wireSessionRuntime(input, assembly, services, session, tools, agentCore, taskRT, contextEngine, summaryCompact)

	// Wire team spawn on the subagent tool. Each spawned teammate gets its
	// own send_message instance — a per-spawn instance keeps the tool
	// stateless from the registry's POV and avoids accidentally sharing a
	// captured ctx across teammates.
	//
	// baseProvider/dynamicProvider hand teammates the leader CURRENT blocks.
	// Byte-identical bytes are the precondition for cross-agent cache reuse, so
	// both are read per spawn rather than captured here.
	if assembly.subagents.tool != nil {
		baseProvider := session.IdentitySystemBlock
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
		spawner := cbteam.Spawner(
			teamReg,
			taskRT,
			extraTools,
			teammateEvents,
			baseProvider,
			dynamicProvider,
			protocol,
			assembly.hookRunner,
			&cbteam.Persist{Roster: services.rosterStore, Transcripts: services.transcripts},
			&cbteam.Isolation{
				RepoRoot: input.cwd,
				Of:       assembly.subagents.isolation,
			},
		)
		assembly.subagents.tool.SetTeamSpawner(spawner)

		// Fan one-shot / background sub-agent runs into the same event hub the
		// teammate spawner feeds, so the live-preview modal lists and streams
		// them too. Teammates publish via the spawner's onEvent; sub-agents via
		// this observer — one hub, two producers.
		assembly.subagents.runner.SetEventObserver(cbteam.SubagentHubObserver(teammateEvents))

		// Lazy teammate resume: boot does NO team work. Instead of eagerly
		// re-spawning prior teammates, each wakes on demand the first time the
		// leader messages it — the waker reuses the same spawner so a woken
		// teammate flows through identical tool injection / transcript recording
		// / roster upsert. A woken teammate joins the active (default) team; the
		// prior team's display name is not restored.
		if services.rosterStore != nil {
			waker := cbteam.NewWaker(spawner, assembly.subagents.runner.AgentConfig, teamReg, services.rosterStore, services.transcripts)
			for _, t := range teamTools {
				if sm, ok := t.(*localtools.SendMessageTool); ok {
					sm.SetWaker(waker)
				}
			}
		}
	}

	pumpCtx, stopPump := context.WithCancel(context.Background())
	go cbteam.NewLeaderInboxPump(teamReg, agentCore, 0).Run(pumpCtx)

	modelName := config.FormatModelID(assembly.settings.Provider, assembly.settings.Model)
	if session != nil && session.ModelName() != "" {
		modelName = session.ModelName()
	}

	rt := &Runtime{
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
		stopTeamPump:   stopPump,

		originalRoots: services.approvalEngine.FilesystemRoots(),
	}

	// Bind the worktree enter/exit tools now the Runtime (their backend) exists.
	wireWorktreeTools(tools, rt)

	rt.Dreamer = wireDream(input, assembly, session, taskRT, sessionID)

	return rt, nil
}

// wireDream attaches background memory consolidation. The dream agent lives
// in a private subagent.Runner: it is invisible to the main model and
// its completion never feeds back into the conversation (no notify). Print
// mode is a one-shot run — no idle hook, no dreamer.
func wireDream(input *resolvedInput, assembly *sessionAssembly, session *agent.Session, taskRT *task.Runtime, sessionID string) *dream.Dreamer {
	if input.nonTTY {
		return nil
	}
	cfg := dream.BuildAgentConfig(input.cwd, assembly.chatModel,
		func(m agentcore.ChatModel) agentcore.ContextManager {
			return newSubAgentContextManager(m, assembly.settings.ContextWindow)
		}, sessionID)
	dreamer := dream.New(dream.Config{
		MemoryDir:      config.MemoryDir(input.cwd),
		SessionsDir:    config.SessionsDir(input.cwd),
		Settings:       assembly.settings.Dream,
		CurrentSession: session.SessionID,
		TaskRT:         taskRT,
		Runner:         subagent.NewRunner(cfg),
	})
	session.SetIdleHook(dreamer.MaybeStart)
	return dreamer
}

// wireWorktreeTools binds the Runtime worktree backend onto the enter/exit
// tools, which were built (in assemble_session) before the Runtime existed.
// localtools can't import bootstrap, so the setters take plain functions.
func wireWorktreeTools(tools []agentcore.Tool, rt *Runtime) {
	for _, t := range tools {
		switch wt := t.(type) {
		case *localtools.EnterWorktreeTool:
			wt.SetEnter(rt.EnterWorktree)
		case *localtools.ExitWorktreeTool:
			wt.SetExit(func(discard bool) (string, error) {
				res, err := rt.ExitWorktree(discard)
				if err != nil {
					return "", err
				}
				return formatWorktreeExitForModel(res), nil
			})
		}
	}
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

// buildContextEngine assembles the four-stage strategy chain, cheapest first.
//
// CommitOnProject is left off, so a threshold-triggered run produces a view for
// that one request and nothing more: the agent's own history — and the session
// file — keep the full tool output. These stages shrink what gets SENT, not
// what is stored. Only the idle path (see idleMicrocompact) and an explicit
// /compact write their result back.
func buildContextEngine(chatModel agentcore.ChatModel, contextWindow, reserveTokens int, cwd string) (*agentctx.ContextEngine, *agentctx.FullSummaryStrategy, *agentctx.ToolResultMicrocompactStrategy) {
	toolCompact := agentctx.NewToolResultMicrocompact(agentctx.ToolResultMicrocompactConfig{
		Classifier:       agent.CodebotToolClassifier,
		KeepRecent:       5,
		ClearedMessageFn: agent.ClearedToolResultMessage,
	})
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
			memoryCompact,
			summaryCompact,
		},
	})
	return engine, summaryCompact, toolCompact
}

func buildAgent(assembly *sessionAssembly, services *bootServices, contextEngine agentcore.ContextManager, tools []agentcore.Tool, sessionID string) (*agentcore.Agent, error) {
	// PreToolUse hooks wrap the permission gate (hooks first, permission
	// decides on the final args — see hooks.WrapGate); PostToolUse stays in
	// the middleware below, which needs around-execution semantics.
	gate := services.approvalEngine.AsToolGate()
	if assembly.hookRunner != nil {
		gate = assembly.hookRunner.WrapGate(gate)
	}
	opts := []agentcore.AgentOption{
		agentcore.WithModel(assembly.chatModel),
		agentcore.WithSystemBlocks(assembly.systemBlocks),
		agentcore.WithTools(tools...),
		agentcore.WithMaxTurns(assembly.settings.MaxTurns),
		agentcore.WithMaxRetries(5),
		agentcore.WithMaxToolErrors(3),
		agentcore.WithMaxToolConcurrency(4),
		agentcore.WithContextManager(contextEngine),
		agentcore.WithToolGate(gate),
		// Place the single message-level cache write breakpoint on the freshest
		// non-system turn (user input, tool_result, or assistant). System
		// blocks 1/2 already carry their own cache_control; this breakpoint
		// covers the growing prefix so each LLM call inside a tool loop reads
		// the previous tool_use+tool_result pair from cache instead of
		// re-uploading them.
		agentcore.WithCacheLastMessage("ephemeral"),
		// Cache routing identity for OpenAI-style providers (Anthropic-style
		// ones use the breakpoints above; the adapter gates by capability).
		// One conversation, one key — Reset/SwitchSession must re-point it
		// via SetPromptCacheKey, see session_runtime.go.
		agentcore.WithPromptCacheKey(sessionID),
	}
	middlewares := make([]agentcore.ToolMiddleware, 0, 2)
	if assembly.telemetryTracer != nil {
		if mw := assembly.telemetryTracer.ToolMiddleware(); mw != nil {
			middlewares = append(middlewares, mw)
		}
	}
	if assembly.hookRunner != nil {
		middlewares = append(middlewares, assembly.hookRunner.Middleware())
	}
	// Innermost, so hooks and telemetry observe the same shortened result the
	// model will see rather than the full output that never reaches it.
	if assembly.outputLimiter != nil {
		middlewares = append(middlewares, assembly.outputLimiter.Middleware())
	}
	if len(middlewares) > 0 {
		opts = append(opts, agentcore.WithMiddlewares(middlewares...))
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
	if input.sessionSnapshot.ReasoningEffort != "" {
		thinking, err := resolveThinkingForModel(assembly.chatModel, input.sessionSnapshot.ReasoningEffort)
		if err != nil {
			return fmt.Errorf("restore reasoning_effort from session: %w", err)
		}
		ag.SetThinkingLevel(agentcore.ThinkingLevel(thinking))
		assembly.settings.ReasoningEffort = thinking
	} else if assembly.settings.ReasoningEffort != "" {
		thinking, err := resolveThinkingForModel(assembly.chatModel, assembly.settings.ReasoningEffort)
		if err != nil {
			return fmt.Errorf("apply reasoning_effort from settings: %w", err)
		}
		ag.SetThinkingLevel(agentcore.ThinkingLevel(thinking))
		assembly.settings.ReasoningEffort = thinking
	}
	return nil
}

func resolveThinkingForModel(model agentcore.ChatModel, level string) (string, error) {
	resolved, ok := provider.ResolveThinkingLevel(model, level)
	if !ok {
		return "", fmt.Errorf("unsupported reasoning_effort %q", level)
	}
	return resolved, nil
}

func buildSession(input *resolvedInput, services *bootServices, assembly *sessionAssembly, contextEngine agentcore.ContextManager, toolCompact *agentctx.ToolResultMicrocompactStrategy, ag *agentcore.Agent, tools []agentcore.Tool) *agent.Session {
	return agent.NewSession(agent.SessionConfig{
		Agent:                 ag,
		ContextManager:        contextEngine,
		ToolMicrocompact:      toolCompact,
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
		Snapshotter:           buildSnapshotter(input.cwd, input.sessionStore.Header().SessionID, assembly.settings.Snapshot),
		FrozenIdentity:        assembly.frozenIdentity,
		FrozenInstructions:    assembly.frozenInstructions,
		InitialMCPOverlay:     assembly.initialMCPOverlay,
		InitialDynamic:        assembly.initialDynamic,
		DeferredToolsPreamble: assembly.deferredToolsPreamble,
		LocalTools:            assembly.localTools,
		SkillAllowsSetter:     services.approvalEngine.SetSkillAllows,
		FileReadState:         assembly.fileReadState,
		TelemetryTracer:       input.telemetryTracer,
		ToolOutputRoot:        config.SessionsDir(input.cwd),
	})
}

// buildSnapshotter returns a workspace snapshotter for /undo, or nil when it is
// disabled. Mirrors opencode's enabled(): off unless the setting is on AND cwd
// is a git repository (the shadow repo borrows git's plumbing, so /undo is
// git-only for now).
func buildSnapshotter(cwd, sessionID string, enabled bool) agent.Snapshotter {
	if !enabled || !config.IsGitRepo(cwd) {
		return nil
	}
	return snapshot.New(config.SnapshotDir(cwd), cwd, config.UndoStatePath(cwd, sessionID))
}

func wireSessionRuntime(input *resolvedInput, assembly *sessionAssembly, services *bootServices, session *agent.Session, tools []agentcore.Tool, ag *agentcore.Agent, taskRT *task.Runtime, contextEngine *agentctx.ContextEngine, summaryCompact *agentctx.FullSummaryStrategy) {
	summaryCompact.SetPostSummaryHooks(session.PostSummaryRecoveryHook())
	contextEngine.SetProjectHook(session.HandleProjectedRewrite)
	contextEngine.SetRecoverHook(session.HandleOverflowRewrite)

	for _, tool := range tools {
		if st, ok := tool.(*localtools.SkillTool); ok {
			st.SetInvocationApplier(session.ApplySkillInvocation)
		}
	}

	assembly.subagents.tool.SetTaskRuntime(taskRT)
	assembly.subagents.tool.SetNotifyFn(session.EnqueueBackgroundResult)

	assembly.outputLimiter.SetOutputDir(session.ToolOutputDir)

	sessionID := input.sessionStore.Header().SessionID
	bgDir := filepath.Join(config.SessionsDir(input.cwd), sessionID, "bg")
	assembly.subagents.tool.SetBgOutputFactory(func(taskID, agentName string) (io.WriteCloser, string, error) {
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
		assembly.bashTool.SetNotifyFn(session.EnqueueBackgroundResult)
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
			session.ReplaceMCPTools(mcpTools)
			session.SetMCPInstructions(strings.Join(services.mcpManager.Instructions(), "\n\n"))
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
