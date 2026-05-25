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
	cbteam "github.com/voocel/codebot/internal/team"
)

// maxAgentNameSuffixAttempts caps the rename retry loop so a registry with an
// implausibly large run of `name-2`, `name-3`, … doesn't spin forever. The
// real ceiling is the model's willingness to spawn that many same-named
// teammates, which is far below this number.
const maxAgentNameSuffixAttempts = 1000

// TeammateSpawner returns a subagent.TeamSpawner closure that turns the
// `subagent { team_name: ... }` tool call into an actual long-lived teammate.
// The closure is bound to the runtime's team registry and task runtime so
// every spawn shares the same coordination surface as send_message / the
// leader inbox pump.
//
// `extraTools` are tools the teammate needs in addition to the ones baked
// into its subagent.Config — today that's send_message, which the filter
// strips from every sub-agent's pool by default. Keeping the list explicit
// rather than reaching into a shared bag avoids accidentally leaking tools
// (like team_create) that should stay leader-only.
func TeammateSpawner(reg *team.Registry, rt *task.Runtime, extraTools []agentcore.Tool, hub *TeammateEventHub) subagent.TeamSpawner {
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
		executor := buildTeammateExecutor(req.Config, tools, model, onEvent)

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
			Protocol:      cbteam.Hooks(),
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
// the team runner expects. Each call:
//
//  1. splits the incoming history+prompt slice — the LAST message is the new
//     user prompt, everything before it is prior history;
//  2. runs AgentLoop with that split, letting the inner loop drive any tool
//     calls until the model hits a stop condition (final answer or MaxTurns);
//  3. collects the produced messages, skipping AgentLoop's initial re-emit
//     of the input prompt so the runner's `history = history + prompt + produced`
//     stitch does not duplicate it.
//
// AgentLoop is otherwise stateless across calls — the runner owns history
// accumulation — but we capture the context manager ONCE at spawn so its
// summary / projection state survives the per-turn boundary. A factory per
// turn would forget any prior compaction and either re-summarise from scratch
// every turn or fail to summarise at all, neither of which is acceptable for
// teammates that may run hundreds of turns.
// onEvent is an optional observer invoked once for every event emitted by
// the teammate's AgentLoop, after the executor has done its own bookkeeping
// (produced collection / error capture). It is the integration seam that
// lets the spawner publish to TeammateEventHub without buildTeammateExecutor
// needing to know about hubs or agent names. nil disables the callback.
func buildTeammateExecutor(cfg subagent.Config, tools []agentcore.Tool, model agentcore.ChatModel, onEvent func(agentcore.Event)) team.TurnExecutor {
	var ctxMgr agentcore.ContextManager
	switch {
	case cfg.ContextManagerFactory != nil:
		ctxMgr = cfg.ContextManagerFactory(model)
	case cfg.ContextManager != nil:
		ctxMgr = cfg.ContextManager
	}

	return func(ctx context.Context, msgs []agentcore.AgentMessage) ([]agentcore.AgentMessage, error) {
		if len(msgs) == 0 {
			return nil, errors.New("teammate executor called with empty messages")
		}
		history := msgs[:len(msgs)-1]
		prompt := msgs[len(msgs)-1]

		// Wrap the teammate system prompt in a SystemBlock with ephemeral
		// cache_control so Anthropic's prompt cache covers it across the
		// teammate's many turns. The system prompt is byte-stable from spawn
		// onward (agent role + tool docs + base instructions), so every
		// follow-up turn after the first reads it from cache instead of
		// paying the full input-token cost.
		//
		// Empty SystemPrompt → leave SystemBlocks nil so AgentLoop falls
		// through its "no system" branch instead of injecting an empty
		// block, which some providers reject.
		var systemBlocks []agentcore.SystemBlock
		if cfg.SystemPrompt != "" {
			systemBlocks = []agentcore.SystemBlock{
				{Text: cfg.SystemPrompt, CacheControl: "ephemeral"},
			}
		}
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
