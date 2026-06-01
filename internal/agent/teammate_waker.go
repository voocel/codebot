package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/storage"
)

// TeammateWaker re-spawns a dormant teammate on demand, seeded with its
// persisted transcript, the first time the leader messages it. Teammate
// recovery is LAZY and message-driven: a stopped teammate is revived only when
// a message targets it, never eagerly mass-restored at session startup.
//
// A teammate that exited — graceful completion, crash, or a prior session that
// ended — leaves two durable traces: its roster entry (who it was + the agent
// type to rebuild its Config from) and its transcript JSONL (what it had done).
// When a message targets that name and no live teammate answers to it, Wake
// rebuilds the teammate from those traces and delivers the message as its
// opening turn — the message itself IS the resume prompt, so no opening message
// is fabricated.
//
// Live control-flow state (in-flight tool calls, pending approvals) is not
// restored; the teammate resumes from its last completed turn.
type TeammateWaker struct {
	spawner     subagent.TeamSpawner
	configOf    func(agentType string) (subagent.Config, bool)
	registry    *team.Registry
	roster      *storage.RosterStore
	transcripts *storage.TranscriptStore

	// mu serialises wakes so two concurrent messages to the same dormant name
	// (the leader can emit parallel send_message calls in one turn) don't each
	// spawn a clone. Wakes are rare, so a single session-wide lock is ample.
	mu sync.Mutex
}

// NewTeammateWaker assembles a waker from the same spawn closure teammates are
// created through (so a woken teammate flows through identical tool injection,
// transcript recording and roster upsert) plus the durable stores. configOf
// rebuilds a teammate's subagent.Config from its agent type; registry is used
// to make wake idempotent under concurrency. A nil spawner, configOf or roster
// makes Wake a permanent no-op so the caller falls back to its normal
// not-found handling.
func NewTeammateWaker(spawner subagent.TeamSpawner, configOf func(agentType string) (subagent.Config, bool), registry *team.Registry, roster *storage.RosterStore, transcripts *storage.TranscriptStore) *TeammateWaker {
	return &TeammateWaker{spawner: spawner, configOf: configOf, registry: registry, roster: roster, transcripts: transcripts}
}

// Wake re-spawns the dormant teammate named `name`, seeding it with its
// persisted transcript and delivering `prompt` as its opening message:
//
//   - (true, nil)  — name matched a persisted roster member and was re-spawned;
//     the message was delivered as its first turn. Caller reports success.
//   - (false, nil) — name is not a known persisted teammate, OR a concurrent
//     wake already revived it. Either way the caller re-checks liveness: live
//     ⇒ deliver via the mailbox; still absent ⇒ fall through to not-found.
//   - (false, err) — name matched a roster member but re-spawn failed (unknown
//     agent type, spawn error); caller surfaces the error.
//
// Wake is idempotent under concurrency: a session-wide lock plus a liveness
// re-check ensures two parallel messages to the same dormant name revive it
// once, not as clones.
func (w *TeammateWaker) Wake(ctx context.Context, name, prompt string) (bool, error) {
	if w == nil || w.spawner == nil || w.configOf == nil || w.roster == nil {
		return false, nil
	}
	member, ok := w.member(name)
	if !ok {
		return false, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// Re-check under the wake lock: a concurrent wake (the leader can emit
	// parallel send_message calls in one turn) may have revived it already.
	// team.Spawn registers the name synchronously, so by the time the prior
	// wake released this lock the name is live. Report not-spawned and let the
	// caller deliver to the now-live mailbox rather than spawning a clone.
	if w.registry != nil {
		if _, live := w.registry.TaskID(name); live {
			return false, nil
		}
	}

	sc, ok := w.configOf(member.AgentType)
	if !ok {
		return false, fmt.Errorf("unknown agent type %q", member.AgentType)
	}

	var history []agentcore.AgentMessage
	if w.transcripts != nil {
		// A read error leaves history nil: the teammate still comes back and can
		// re-claim work from the shared task list, just without prior context.
		history, _ = w.transcripts.Load(name)
	}

	// TeamName is left empty so the teammate joins whatever team is active in
	// this session — the default team is pre-created at startup and renamed to
	// the prior team's identity on resume, so an explicit name would only risk
	// a spurious mismatch.
	if _, err := w.spawner(ctx, subagent.TeamSpawnRequest{
		Config:        sc,
		Name:          name,
		InitialPrompt: prompt,
		Description:   member.Description,
		Color:         member.Color,
		History:       history,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// member returns the persisted roster entry for name, if any.
func (w *TeammateWaker) member(name string) (storage.RosterMember, bool) {
	for _, m := range w.roster.Snapshot().Members {
		if m.Name == name {
			return m, true
		}
	}
	return storage.RosterMember{}, false
}
