package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/config"
)

// maxAgentNameSuffixAttempts caps the rename retry loop so a registry with an
// implausibly large run of `name-2`, `name-3`, … doesn't spin forever. The
// real ceiling is the model's willingness to spawn that many same-named
// teammates, which is far below this number.
const maxAgentNameSuffixAttempts = 1000

// TeammateSpawner returns the subagent.TeamSpawner closure that turns a
// `subagent { team_name: ... }` tool call into a long-lived teammate.
// Bound to the runtime's team registry + task runtime so every spawn
// shares the same coordination surface (send_message / leader inbox pump).
//
// Parameters:
//   - extraTools: force-injected on top of req.Config.Tools (send_message
//     and the shared task tools) — listed explicitly to avoid leaking
//     leader-only tools.
//   - hub: per-session teammate event fan-out; nil disables observation.
//   - baseBlocks: universal base prefix shared with the leader for prompt
//     cache reuse in Default/Append modes; nil ⇒ role block only.
//   - dynamicProvider: invoked once per spawn to snapshot the leader's
//     current dynamic block (MCP + overlays). Snapshot is frozen at spawn;
//     nil skips dynamic propagation.
//   - protocol: fully wired ProtocolHooks (envelope, idle notification,
//     priority, optional IdleClaim). Bootstrap owns the wiring.
func TeammateSpawner(reg *team.Registry, rt *task.Runtime, extraTools []agentcore.Tool, hub *TeammateEventHub, baseBlocks []agentcore.SystemBlock, dynamicProvider func() *agentcore.SystemBlock, protocol team.ProtocolHooks) subagent.TeamSpawner {
	return func(ctx context.Context, req subagent.TeamSpawnRequest) (*subagent.TeamSpawnResult, error) {
		if reg == nil || rt == nil {
			return nil, errors.New("teammate spawner: registry and runtime are required")
		}
		teamCtx := reg.Team()
		if teamCtx == nil {
			return nil, errors.New("no active team — call team_create first")
		}
		// team_name is optional: a default team is pre-created at session
		// startup, so most callers just want to drop the teammate into the
		// active team. Only validate when the model explicitly named a team.
		if req.TeamName != "" && req.TeamName != teamCtx.Name {
			return nil, fmt.Errorf("team_name %q does not match the active team %q", req.TeamName, teamCtx.Name)
		}

		model := req.Model
		if model == nil {
			model = req.Config.Model
		}
		if model == nil {
			return nil, fmt.Errorf("agent %q has no model configured and no override given", req.Config.Name)
		}

		tools := mergeTeammateTools(req.Config.Tools, extraTools)

		// Resolve a unique name BEFORE building the executor so the hub
		// publishes under the final id; the model is told the same id back.
		agentName := uniqueAgentName(reg, req.Name)

		// onEvent fans every AgentLoop event into the hub. Capturing
		// agentName in the closure keeps buildTeammateExecutor agnostic of
		// hub/name plumbing — it only knows "tell me about each event".
		var onEvent func(agentcore.Event)
		if hub != nil {
			onEvent = func(ev agentcore.Event) {
				hub.Publish(agentName, ev)
			}
		}
		// Snapshot the dynamic block once per spawn — the teammate freezes it
		// at construction and ignores subsequent leader-side changes.
		var dynamicBlock *agentcore.SystemBlock
		if dynamicProvider != nil {
			dynamicBlock = dynamicProvider()
		}
		executor := buildTeammateExecutor(req.Config, tools, model, onEvent, baseBlocks, dynamicBlock)

		depth := task.DepthFromContext(ctx) + 1
		if depth > task.MaxAgentDepth {
			return nil, fmt.Errorf("agent nesting depth %d exceeds max %d", depth, task.MaxAgentDepth)
		}

		// Spawn off background context, not the caller's tool-call ctx: the
		// teammate must outlive the leader's current turn. Session-level
		// shutdown is handled by Runtime.Close (DeleteTeam closes mailboxes
		// → Run exits) and by task.Runtime.StopAll.
		spawnCtx := task.WithDepth(context.Background(), depth)

		// onExit flips the hub's active flag when the teammate goroutine
		// returns. The history ring is preserved so an observer can still
		// open this teammate's transcript afterwards; the UI distinguishes
		// "live" vs "ended" via hub.IsActive.
		var onExit func(error)
		if hub != nil {
			onExit = func(error) { hub.MarkStopped(agentName) }
		}

		res, err := team.Spawn(spawnCtx, team.SpawnConfig{
			AgentName:     agentName,
			InitialPrompt: req.InitialPrompt,
			Description:   req.Description,
			Color:         req.Color,
			Registry:      reg,
			TaskRT:        rt,
			Execute:       executor,
			Protocol:      protocol,
			Depth:         depth,
			OnExit:        onExit,
		})
		if err != nil {
			return nil, err
		}
		return &subagent.TeamSpawnResult{
			TaskID:  res.TaskID,
			AgentID: res.AgentID,
		}, nil
	}
}

// uniqueAgentName returns base when no live agent in reg already uses it
// (case-insensitive); otherwise appends "-2", "-3", … until it finds a free
// slot. Comparison is case-insensitive so the model can't accidentally split
// a logical "Tester" + "tester" into two routing targets.
//
// There is a benign TOCTOU window between this check and team.Spawn's own
// RegisterAgent: a concurrent spawn could grab the chosen name first. Spawn
// itself will then return ErrAgentExists and the caller sees the original
// error path — this helper only optimises the common single-leader case.
func uniqueAgentName(reg *team.Registry, base string) string {
	if reg == nil || base == "" {
		return base
	}
	taken := make(map[string]bool)
	for _, n := range reg.AgentNames() {
		taken[strings.ToLower(n)] = true
	}
	if !taken[strings.ToLower(base)] {
		return base
	}
	for i := 2; i < maxAgentNameSuffixAttempts; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
	// Fall through to base; team.Spawn will surface ErrAgentExists cleanly.
	return base
}

// mergeTeammateTools appends extras to base, skipping any duplicates by tool
// Name. Duplicates would happen if a custom agent definition explicitly lists
// a tool that the spawner also force-injects.
func mergeTeammateTools(base, extras []agentcore.Tool) []agentcore.Tool {
	if len(extras) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base))
	for _, t := range base {
		seen[t.Name()] = true
	}
	out := make([]agentcore.Tool, 0, len(base)+len(extras))
	out = append(out, base...)
	for _, t := range extras {
		if seen[t.Name()] {
			continue
		}
		seen[t.Name()] = true
		out = append(out, t)
	}
	return out
}

// buildTeammateExecutor wraps agentcore.AgentLoop in the TurnExecutor shape
// the team runner expects. Each turn splits msgs into history + new prompt
// (last entry), runs AgentLoop, and returns the produced messages (skipping
// AgentLoop's initial echo of the input prompt to avoid duplicating it in
// the runner's stitched history).
//
// The context manager is captured ONCE here, not per-turn, so summary /
// projection state survives across turns — teammates may run hundreds of
// turns and re-summarising from scratch each one is not acceptable.
//
// SystemBlocks are also assembled once: cfg + tools + baseBlocks +
// dynamicBlock are all fixed for the teammate's lifetime, so rebuilding
// per turn would produce identical bytes for no benefit.
//
// onEvent fans every AgentLoop event to an external observer after the
// executor's own bookkeeping; nil disables.
//
// Identity (AgentName / TeamName / Color) is NOT plumbed through here —
// agentcore/team.Runner wraps every Execute call with WithIdentity, so
// tools that vary by caller read it from coreteam.IdentityFromContext.
func buildTeammateExecutor(cfg subagent.Config, tools []agentcore.Tool, model agentcore.ChatModel, onEvent func(agentcore.Event), baseBlocks []agentcore.SystemBlock, dynamicBlock *agentcore.SystemBlock) team.TurnExecutor {
	var ctxMgr agentcore.ContextManager
	switch {
	case cfg.ContextManagerFactory != nil:
		ctxMgr = cfg.ContextManagerFactory(model)
	case cfg.ContextManager != nil:
		ctxMgr = cfg.ContextManager
	}

	// Assemble SystemBlocks once at spawn — depends only on cfg + tools +
	// baseBlocks + dynamicBlock, all of which are fixed for the teammate's
	// lifetime. Doing it inside the per-turn closure would rebuild identical
	// bytes every wake-up for no benefit.
	systemBlocks := assembleTeammateSystemBlocks(cfg, tools, baseBlocks, dynamicBlock)

	return func(ctx context.Context, msgs []agentcore.AgentMessage) ([]agentcore.AgentMessage, error) {
		if len(msgs) == 0 {
			return nil, errors.New("teammate executor called with empty messages")
		}
		history := msgs[:len(msgs)-1]
		prompt := msgs[len(msgs)-1]

		agentCtx := agentcore.AgentContext{
			SystemBlocks: systemBlocks,
			Tools:        tools,
			Messages:     append([]agentcore.AgentMessage(nil), history...),
		}
		loopCfg := agentcore.LoopConfig{
			Model:          model,
			MaxTurns:       cfg.MaxTurns,
			MaxRetries:     cfg.MaxRetries,
			ContextManager: ctxMgr,
			ConvertToLLM:   cfg.ConvertToLLM,
		}

		events := agentcore.AgentLoop(ctx, []agentcore.AgentMessage{prompt}, agentCtx, loopCfg)

		var (
			produced []agentcore.AgentMessage
			loopErr  error
			// AgentLoop emits MessageStart+MessageEnd for each input prompt
			// before running the actual loop. Skip exactly one MessageEnd
			// (we pass exactly one prompt) so we don't duplicate it in the
			// runner's history.
			promptEndsToSkip = 1
		)
		for ev := range events {
			switch ev.Type {
			case agentcore.EventMessageEnd:
				if promptEndsToSkip > 0 {
					promptEndsToSkip--
				} else if ev.Message != nil {
					produced = append(produced, ev.Message)
				}
			case agentcore.EventError:
				if ev.Err != nil {
					loopErr = ev.Err
				}
			}
			// Fan out AFTER bookkeeping so observer drops never cause us to
			// lose a produced message. onEvent is required to be
			// non-blocking by contract (the hub uses drop-oldest).
			if onEvent != nil {
				onEvent(ev)
			}
		}
		return produced, loopErr
	}
}

// assembleTeammateSystemBlocks composes the teammate's SystemBlocks based on
// cfg.SystemPromptMode:
//
//   - Default: baseBlocks + role block (cfg.SystemPrompt wrapped under
//     "# Custom Agent Instructions" when set) + dynamicBlock. Teammate
//     inherits host conventions and shares the cache prefix with the leader.
//   - Replace: a single SystemBlock with cfg.SystemPrompt verbatim — for
//     fully isolated agents that already include everything they need.
//   - Append: Default + cfg.SystemPrompt joined to the role block (NOT a
//     separate cache block — caps on system cache_control are tight).
//
// Unknown mode values fall through to Default. Dynamic block placement is
// always AFTER the cache-controlled role block with no cache_control of its
// own, matching the leader's frozen/dynamic split.
func assembleTeammateSystemBlocks(cfg subagent.Config, tools []agentcore.Tool, baseBlocks []agentcore.SystemBlock, dynamicBlock *agentcore.SystemBlock) []agentcore.SystemBlock {
	switch cfg.SystemPromptMode {
	case config.SystemPromptModeReplace:
		if cfg.SystemPrompt == "" {
			return nil
		}
		return []agentcore.SystemBlock{
			{Text: cfg.SystemPrompt, CacheControl: "ephemeral"},
		}

	case config.SystemPromptModeAppend:
		role := config.BuildTeammateRoleBlock(toolInfosFromTools(tools), "")
		if cfg.SystemPrompt != "" {
			role = role + "\n\n" + cfg.SystemPrompt
		}
		out := make([]agentcore.SystemBlock, 0, len(baseBlocks)+2)
		out = append(out, baseBlocks...)
		out = append(out, agentcore.SystemBlock{Text: role, CacheControl: "ephemeral"})
		if dynamicBlock != nil && dynamicBlock.Text != "" {
			out = append(out, *dynamicBlock)
		}
		return out

	default:
		// Default mode (empty string, "default", or any unrecognized value).
		role := config.BuildTeammateRoleBlock(toolInfosFromTools(tools), cfg.SystemPrompt)
		out := make([]agentcore.SystemBlock, 0, len(baseBlocks)+2)
		out = append(out, baseBlocks...)
		out = append(out, agentcore.SystemBlock{Text: role, CacheControl: "ephemeral"})
		if dynamicBlock != nil && dynamicBlock.Text != "" {
			out = append(out, *dynamicBlock)
		}
		return out
	}
}

// toolInfosFromTools projects agentcore.Tool into the lighter config.ToolInfo
// shape that prompt builders accept. Returns nil for an empty input so the
// builder's "no tools section" branch fires cleanly.
func toolInfosFromTools(tools []agentcore.Tool) []config.ToolInfo {
	if len(tools) == 0 {
		return nil
	}
	out := make([]config.ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, config.ToolInfo{Name: t.Name(), Description: t.Description()})
	}
	return out
}
