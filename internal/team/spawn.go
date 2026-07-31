package team

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	coreteam "github.com/voocel/agentcore/team"
	agenttools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/worktree"
)

// WorktreeIsolation is the AgentDefinition.Isolation value that opts a teammate
// into a private git worktree.
const WorktreeIsolation = "worktree"

// Isolation configures optional per-teammate git-worktree sandboxing.
// A nil *Isolation disables it entirely — every teammate shares the
// leader's cwd, the pre-Phase-2 behaviour. When set, a teammate whose agent
// type maps to "worktree" in Of runs in its own checkout so its writes cannot
// clobber a peer editing the same files. The checkout is bound at spawn by a
// cwd override on the teammate's context (see teammateCwd), not by rebuilding
// its tools — the same tool instances resolve paths against whatever cwd the
// running context carries.
type Isolation struct {
	// RepoRoot is the git repository the sandboxes branch from (the leader cwd).
	RepoRoot string
	// Of maps agent type (subagent.Config.Name) to its isolation mode; an
	// absent key means shared. Only "worktree" triggers a sandbox.
	Of map[string]string
}

// declares reports whether the agent type opted into worktree isolation. It
// deliberately does NOT check that the repo is a git repository: a declared
// isolation that can't be honoured must fail loudly (see the spawner), never
// silently fall back to the shared cwd where the teammate could clobber peers.
func (ti *Isolation) declares(agentType string) bool {
	return ti != nil && ti.Of[agentType] == WorktreeIsolation
}

// teammateCwd returns the working directory a spawned teammate runs in: its own
// worktree when isolated, otherwise the leader's current cwd — which rides in on
// the spawn call's ctx (the leader injects it via Session.baseRunCtx), so a teammate
// spawned while the leader is inside a worktree shares that worktree. An empty
// result makes the spawnCtx WithCwd a no-op, falling back to the tools'
// constructed WorkDir.
func teammateCwd(wt *teammateWorktree, callerCtx context.Context) string {
	if wt != nil {
		return wt.dir
	}
	return agenttools.CwdFromContext(callerCtx)
}

// teammateWorktree is the sandbox a single isolated teammate runs in, retained
// so onExit can clean it up.
type teammateWorktree struct {
	repoRoot string
	dir      string
	branch   string
}

// newTeammateWorktree creates (or reclaims, on wake) the sandbox for agentName
// and seeds it with the gitignored files a clean checkout lacks. A copy failure
// is surfaced on stderr — the sandbox still works, but the user should know it
// may be missing local config.
func newTeammateWorktree(repoRoot, agentName string) (*teammateWorktree, error) {
	dir, branch, err := worktree.CreateOrReuse(repoRoot, worktree.Slug("wt-"+agentName))
	if err != nil {
		return nil, err
	}
	if failed, cerr := worktree.CopyIncludes(repoRoot, dir, worktree.DefaultIncludes); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: teammate worktree %s: copy local files: %v\n", dir, cerr)
	} else if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "warning: teammate worktree %s could not copy local files: %s\n", dir, strings.Join(failed, ", "))
	}
	return &teammateWorktree{repoRoot: repoRoot, dir: dir, branch: branch}, nil
}

// teammateBaseBlocks returns the system base a teammate is built with.
//
// baseProvider is read per spawn, never captured at boot: a shared teammate
// follows the leader cwd (see teammateCwd), which a worktree enter rewrites
// along with block 1.
//
// An isolated teammate instead rebuilds the base for its own worktree — its
// tools resolve against the sandbox and run with no approval gate, so a stale
// path would invite absolute-path writes escaping into the main tree. An empty
// shared base (SystemOverride) passes through: that authored prompt must not
// be displaced.
func teammateBaseBlocks(baseProvider func() []agentcore.SystemBlock, wt *teammateWorktree) []agentcore.SystemBlock {
	var shared []agentcore.SystemBlock
	if baseProvider != nil {
		shared = baseProvider()
	}
	if wt == nil || len(shared) == 0 {
		return shared
	}
	return []agentcore.SystemBlock{
		{Text: config.BuildUniversalBase(wt.dir), CacheControl: "ephemeral"},
	}
}

// cleanup runs when the isolated teammate exits, mirroring the leader-side
// ExitWorktree "keep the scene" policy:
//
//   - Uncommitted changes (or a status check that errored) → keep the whole
//     sandbox and tell the leader where the work is.
//   - Clean tree → non-force Remove, which is data-safe: if the teammate had
//     committed into the branch, git keeps the branch (branchKept) and only the
//     checkout is dropped; the leader is told the commits survived there.
//   - Clean tree, nothing committed → fully removed, silently.
func (wt *teammateWorktree) cleanup(reg *coreteam.Registry, agentName string) {
	changed, err := worktree.HasChanges(wt.dir)
	if err == nil && !changed {
		branchKept, rerr := worktree.Remove(wt.repoRoot, wt.dir, wt.branch, false)
		switch {
		case rerr != nil:
			// Removal failed — the checkout is most likely still on disk. Don't
			// claim it was removed; tell the leader to look.
			wt.notifyLeader(reg, agentName, fmt.Sprintf(
				"cleanup of my worktree failed; it is kept at %s (branch %s) — inspect manually: %v", wt.dir, wt.branch, rerr))
		case branchKept:
			wt.notifyLeader(reg, agentName, fmt.Sprintf(
				"My worktree was clean so I removed the checkout, but branch %s has commits not merged elsewhere and was kept. Inspect with: git log %s",
				wt.branch, wt.branch))
		default:
			// Clean and nothing committed — gone without a trace, nothing to report.
		}
		return
	}
	wt.notifyLeader(reg, agentName, fmt.Sprintf(
		"I left uncommitted changes in worktree %s (branch %s). Review with: git -C %s diff", wt.dir, wt.branch, wt.dir))
}

// notifyLeader best-effort delivers a message to the team lead's mailbox.
func (wt *teammateWorktree) notifyLeader(reg *coreteam.Registry, agentName, text string) {
	if reg == nil {
		return
	}
	if mb := reg.Mailbox(coreteam.TeamLeadName); mb != nil {
		_ = mb.Send(coreteam.Message{From: agentName, Text: text})
	}
}

// Persist bundles the durable stores a spawned teammate writes to so
// the session can recover its team after a restart: the roster (who is on the
// team + how to re-spawn them) and the per-teammate conversation transcript.
// A nil *Persist — or nil fields within — disables that slice of
// persistence, so tests and ephemeral sessions can omit it entirely.
type Persist struct {
	Roster      *storage.RosterStore
	Transcripts *storage.TranscriptStore
}

// maxAgentNameSuffixAttempts caps the rename retry loop so a registry with an
// implausibly large run of `name-2`, `name-3`, … doesn't spin forever. The
// real ceiling is the model's willingness to spawn that many same-named
// teammates, which is far below this number.
const maxAgentNameSuffixAttempts = 1000

// Spawner returns the subagent.TeamSpawner closure that turns a
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
//   - isolation: optional per-teammate git-worktree sandboxing. nil keeps every
//     teammate in the leader's shared cwd; when set, a teammate whose agent type
//     opts into "worktree" runs in its own checkout (bound via a cwd override on
//     the spawn context).
func Spawner(reg *coreteam.Registry, rt *task.Runtime, extraTools []agentcore.Tool, hub *EventHub, baseProvider func() []agentcore.SystemBlock, dynamicProvider func() *agentcore.SystemBlock, protocol coreteam.ProtocolHooks, hookRunner *hooks.Runner, persist *Persist, isolation *Isolation) subagent.TeamSpawner {
	// worktreeMu serialises sandbox creation: the leader can fire parallel
	// subagent spawns (agentcore runs tools concurrently), and concurrent
	// `git worktree add` on one repo contend on the index lock — a loser would
	// degrade to the shared cwd. Spawns are infrequent and creation is fast, so
	// a per-session lock costs nothing meaningful.
	var worktreeMu sync.Mutex
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

		// Git-worktree isolation: when this agent type opts in, the teammate runs
		// in its own sandbox so its writes can't clobber a peer working the same
		// files. The checkout is bound to the teammate via a cwd override on its
		// spawnCtx (see teammateCwd) — the same tool instances then resolve paths
		// against the sandbox; every other tool (send_message, task_*, MCP) is
		// untouched. This is fail-closed — if isolation was declared but can't be
		// honoured (not a git repo, or the checkout fails), the spawn errors
		// rather than silently running in the shared cwd, which is exactly the
		// clobbering the agent asked to avoid.
		var wt *teammateWorktree
		if isolation.declares(req.Config.Name) {
			if !config.IsGitRepo(isolation.RepoRoot) {
				return nil, fmt.Errorf("teammate %q declares worktree isolation but %s is not a git repository", agentName, isolation.RepoRoot)
			}
			worktreeMu.Lock()
			w, err := newTeammateWorktree(isolation.RepoRoot, agentName)
			worktreeMu.Unlock()
			if err != nil {
				return nil, fmt.Errorf("worktree isolation for teammate %q: %w", agentName, err)
			}
			wt = w
		}

		// onEvent fans every AgentLoop event into the hub. Capturing
		// agentName in the closure keeps buildTeammateExecutor agnostic of
		// hub/name plumbing — it only knows "tell me about each event".
		var onEvent func(agentcore.Event)
		if hub != nil {
			onEvent = func(ev agentcore.Event) {
				hub.Publish(agentName, ev)
			}
		}
		var commitMessage func(agentcore.AgentMessage) error
		if persist != nil && persist.Transcripts != nil {
			commitMessage = func(message agentcore.AgentMessage) error {
				if err := persist.Transcripts.Append(agentName, []agentcore.AgentMessage{message}); err != nil {
					return fmt.Errorf("append transcript for %q: %w", agentName, err)
				}
				return nil
			}
		}
		// Snapshot the dynamic block once per spawn — the teammate freezes it
		// at construction and ignores subsequent leader-side changes.
		var dynamicBlock *agentcore.SystemBlock
		if dynamicProvider != nil {
			dynamicBlock = dynamicProvider()
		}

		// One teammate, one cache lineage: suffix the unique teammate name so
		// same-type teammates don't share a routing bucket, while a resumed
		// teammate (same name) keeps its warm cache.
		executor := buildTeammateExecutor(req.Config, tools, model, onEvent, commitMessage, teammateBaseBlocks(baseProvider, wt), dynamicBlock, PromptCacheKey(req.Config.PromptCacheKey, agentName))

		depth := task.DepthFromContext(ctx) + 1
		if depth > task.MaxAgentDepth {
			return nil, fmt.Errorf("agent nesting depth %d exceeds max %d", depth, task.MaxAgentDepth)
		}

		// Spawn off background context, not the caller's tool-call ctx: the
		// teammate must outlive the leader's current turn. Session-level
		// shutdown is handled by Runtime.Close (DeleteTeam closes mailboxes
		// → Run exits) and by task.Runtime.StopAll. The cwd override is carried
		// over explicitly (the background ctx drops the caller's values), so the
		// teammate's tools resolve against its sandbox / the leader's worktree.
		spawnCtx := agenttools.WithCwd(task.WithDepth(context.Background(), depth), teammateCwd(wt, ctx))

		// onExit flips the hub's active flag and fires the SubagentStop hook
		// when the teammate goroutine returns. The history ring is preserved so
		// an observer can still open this teammate's transcript afterwards; the
		// UI distinguishes "live" vs "ended" via hub.IsActive.
		var onExit func(error)
		if hub != nil || hookRunner != nil || wt != nil {
			onExit = func(error) {
				if hub != nil {
					hub.MarkStopped(agentName)
				}
				if hookRunner != nil {
					hookRunner.RunSubagentStop(context.Background(), agentName)
				}
				if wt != nil {
					wt.cleanup(reg, agentName)
				}
			}
		}

		res, err := coreteam.Spawn(spawnCtx, coreteam.SpawnConfig{
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
// There is a benign TOCTOU window between this check and coreteam.Spawn's own
// RegisterAgent: a concurrent spawn could grab the chosen name first. Spawn
// itself will then return ErrAgentExists and the caller sees the original
// error path — this helper only optimises the common single-leader case.
func uniqueAgentName(reg *coreteam.Registry, base string) string {
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
	// Fall through to base; coreteam.Spawn will surface ErrAgentExists cleanly.
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
// executor's own bookkeeping; nil disables. commitMessage runs synchronously
// before a message enters context or starts a requested tool; teammate
// transcript persistence is attached here.
//
// Identity (AgentName / TeamName / Color) is NOT plumbed through here —
// agentcore/team.Runner wraps every Execute call with WithIdentity, so
// tools that vary by caller read it from coreteam.IdentityFromContext.
func buildTeammateExecutor(cfg subagent.Config, tools []agentcore.Tool, model agentcore.ChatModel, onEvent func(agentcore.Event), commitMessage func(agentcore.AgentMessage) error, baseBlocks []agentcore.SystemBlock, dynamicBlock *agentcore.SystemBlock, promptCacheKey string) coreteam.TurnExecutor {
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
			CommitMessage:  commitMessage,
			// Prompt caching pays off most here: a teammate is a long-lived
			// conversation that replays its full history on every wake-up.
			// The rolling breakpoint adds a third cache_control on top of the
			// two frozen system blocks (Anthropic allows 4 per request); the
			// routing key keeps all wake-ups on one cache shard.
			CacheLastMessage: cfg.CacheLastMessage,
			PromptCacheKey:   promptCacheKey,
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

// PromptCacheKey derives a spawned agent's cache routing base from the
// session identity. One conversation, one key: agentcore appends "#<seq>" per
// spawn, so runs of the same definition don't pile into a single routing
// bucket. Shared by teammate spawns and subagent definitions (agent_build).
func PromptCacheKey(sessionID, agentName string) string {
	if sessionID == "" {
		return ""
	}
	return sessionID + "-" + agentName
}
