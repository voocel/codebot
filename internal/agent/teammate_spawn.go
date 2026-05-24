package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	cbteam "github.com/voocel/codebot/internal/team"
)

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
func TeammateSpawner(reg *team.Registry, rt *task.Runtime, extraTools []agentcore.Tool) subagent.TeamSpawner {
	return func(ctx context.Context, req subagent.TeamSpawnRequest) (*subagent.TeamSpawnResult, error) {
		if reg == nil || rt == nil {
			return nil, errors.New("teammate spawner: registry and runtime are required")
		}
		teamCtx := reg.Team()
		if teamCtx == nil {
			return nil, errors.New("no active team — call team_create first")
		}
		if req.TeamName != teamCtx.Name {
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
		executor := buildTeammateExecutor(req.Config, tools, model)

		depth := task.DepthFromContext(ctx) + 1
		if depth > task.MaxAgentDepth {
			return nil, fmt.Errorf("agent nesting depth %d exceeds max %d", depth, task.MaxAgentDepth)
		}

		// Spawn off background context, not the caller's tool-call ctx: the
		// teammate must outlive the leader's current turn. Session-level
		// shutdown is handled by Runtime.Close (DeleteTeam closes mailboxes
		// → Run exits) and by task.Runtime.StopAll.
		spawnCtx := task.WithDepth(context.Background(), depth)

		res, err := team.Spawn(spawnCtx, team.SpawnConfig{
			AgentName:     req.Name,
			InitialPrompt: req.InitialPrompt,
			Description:   req.Description,
			Color:         req.Color,
			Registry:      reg,
			TaskRT:        rt,
			Execute:       executor,
			Protocol:      cbteam.Hooks(),
			Depth:         depth,
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
func buildTeammateExecutor(cfg subagent.Config, tools []agentcore.Tool, model agentcore.ChatModel) team.TurnExecutor {
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

		agentCtx := agentcore.AgentContext{
			SystemPrompt: cfg.SystemPrompt,
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
					continue
				}
				if ev.Message != nil {
					produced = append(produced, ev.Message)
				}
			case agentcore.EventError:
				if ev.Err != nil {
					loopErr = ev.Err
				}
			}
		}
		return produced, loopErr
	}
}
