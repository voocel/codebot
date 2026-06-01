package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/storage"
)

// TeammatePersist bundles the durable stores a spawned teammate writes to so
// the session can recover its team after a restart: the roster (who is on the
// team + how to re-spawn them) and the per-teammate conversation transcript.
// A nil *TeammatePersist — or nil fields within — disables that slice of
// persistence, so tests and ephemeral sessions can omit it entirely.
type TeammatePersist struct {
	Roster      *storage.RosterStore
	Transcripts *storage.TranscriptStore
}

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
//   - hookRunner: fires the SubagentStop lifecycle hook when a teammate
//     exits; nil disables it.
//   - persist: durable roster + transcript stores. On a successful spawn the
//     teammate is recorded in the roster and every turn is appended to its
//     transcript, so a restart can re-spawn it with its prior context. nil
//     disables persistence.
func TeammateSpawner(reg *team.Registry, rt *task.Runtime, extraTools []agentcore.Tool, hub *TeammateEventHub, baseBlocks []agentcore.SystemBlock, dynamicProvider func() *agentcore.SystemBlock, protocol team.ProtocolHooks, hookRunner *hooks.Runner, persist *TeammatePersist) subagent.TeamSpawner {
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

		// Wrap the executor to append each turn (the prompt the runner fed +
		// the messages produced) to the teammate's transcript. This is the
		// lossless capture point — identical to what the runner stitches into
		// its history — unlike the drop-oldest event hub. Best-effort: a write
		// failure is logged but never fails the turn.
		if persist != nil && persist.Transcripts != nil {
			base := executor
			executor = func(ctx context.Context, msgs []agentcore.AgentMessage) ([]agentcore.AgentMessage, error) {
				produced, err := base(ctx, msgs)
				if len(msgs) > 0 {
					turn := make([]agentcore.AgentMessage, 0, 1+len(produced))
					turn = append(turn, msgs[len(msgs)-1])
					turn = append(turn, produced...)
					if werr := persist.Transcripts.Append(agentName, turn); werr != nil {
						fmt.Fprintf(os.Stderr, "warning: transcript append for %q: %v\n", agentName, werr)
					}
				}
				return produced, err
			}
		}

		depth := task.DepthFromContext(ctx) + 1
		if depth > task.MaxAgentDepth {
			return nil, fmt.Errorf("agent nesting depth %d exceeds max %d", depth, task.MaxAgentDepth)
		}

		// Spawn off background context, not the caller's tool-call ctx: the
		// teammate must outlive the leader's current turn. Session-level
		// shutdown is handled by Runtime.Close (DeleteTeam closes mailboxes
		// → Run exits) and by task.Runtime.StopAll.
		spawnCtx := task.WithDepth(context.Background(), depth)

		// onExit flips the hub's active flag and fires the SubagentStop hook
		// when the teammate goroutine returns. The history ring is preserved so
		// an observer can still open this teammate's transcript afterwards; the
		// UI distinguishes "live" vs "ended" via hub.IsActive.
		var onExit func(error)
		if hub != nil || hookRunner != nil {
			onExit = func(error) {
				if hub != nil {
					hub.MarkStopped(agentName)
				}
				if hookRunner != nil {
					hookRunner.RunSubagentStop(context.Background(), agentName)
				}
			}
		}

		res, err := team.Spawn(spawnCtx, team.SpawnConfig{
			AgentName:     agentName,
			InitialPrompt: req.InitialPrompt,
			History:       req.History,
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

		// Record the teammate in the roster so a restart knows it existed and
		// how to re-spawn it. AgentType is the definition key the resume path
		// rebuilds Config from; model / system prompt / tools all come back via
		// that lookup, so they are not duplicated here.
		if persist != nil && persist.Roster != nil {
			persist.Roster.SetTeam(teamCtx.Name, teamCtx.Description)
			persist.Roster.UpsertMember(storage.RosterMember{
				Name:          agentName,
				AgentType:     req.Config.Name,
				Color:         req.Color,
				InitialPrompt: req.InitialPrompt,
				Description:   req.Description,
				Depth:         depth,
				Kind:          "teammate",
			})
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
