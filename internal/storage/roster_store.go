package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rosterFile is the single JSON document a RosterStore persists under its dir.
const rosterFile = "roster.json"

// RosterMember is the minimal, faithful record needed to re-spawn one teammate
// after a restart. It deliberately stores only spawn-time facts that are NOT
// recoverable from the agent definition registry:
//
//   - Name is the unique routing identifier (post de-duplication), distinct
//     from AgentType when the leader spawned two of the same kind.
//   - AgentType is the definition key (subagent.Config.Name) the harness
//     rebuilds the full Config from on resume — model, system prompt, tools
//     and context manager all come back via that lookup, so they are not
//     duplicated here.
//   - Color / InitialPrompt / Description / Depth are per-spawn overrides the
//     definition does not carry.
//   - Kind tags the agent's spawn mode. Today every team-spawned agent is a
//     teammate; the field exists so restore/UI can branch once background or
//     one-shot agents also land in the roster. Empty ⇒ treat as teammate.
type RosterMember struct {
	Name          string `json:"name"`
	AgentType     string `json:"agent_type"`
	Color         string `json:"color,omitempty"`
	InitialPrompt string `json:"initial_prompt,omitempty"`
	Description   string `json:"description,omitempty"`
	Depth         int    `json:"depth,omitempty"`
	Kind          string `json:"kind,omitempty"`
}

// Roster is the persisted team snapshot: the active team's identity plus every
// live teammate. It mirrors the in-memory team.Registry roster so a restarted
// session can rebuild the team and re-spawn its members.
type Roster struct {
	Team        string         `json:"team"`
	Description string         `json:"description,omitempty"`
	Members     []RosterMember `json:"members,omitempty"`
}

// RosterStore persists a single team Roster to <dir>/roster.json with atomic
// writes. Like TaskStore it is safe for in-memory-only use: with no dir set
// every mutation is kept in memory and nothing touches disk. The directory is
// created lazily on the first write so sessions that never form a team leave
// nothing behind.
type RosterStore struct {
	mu     sync.Mutex
	dir    string // persistence directory; empty = in-memory only
	roster Roster
}

// NewRosterStore returns an empty in-memory store. Call SetDir to enable
// persistence (and load any prior roster).
func NewRosterStore() *RosterStore {
	return &RosterStore{}
}

// SetDir enables file persistence and loads an existing roster.json if present.
// A missing dir or file is not an error — it just means no prior team.
func (s *RosterStore) SetDir(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dir = dir
	return s.loadLocked()
}

func (s *RosterStore) loadLocked() error {
	data, err := os.ReadFile(filepath.Join(s.dir, rosterFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no prior team
		}
		return fmt.Errorf("read roster: %w", err)
	}
	var r Roster
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("parse roster: %w", err)
	}
	s.roster = r
	return nil
}

// SetTeam records the active team's name and description, replacing any prior
// team. Members are preserved (rename/describe must not drop the roster); use
// Clear to drop a team entirely.
func (s *RosterStore) SetTeam(name, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roster.Team = name
	s.roster.Description = description
	s.persistLocked()
}

// UpsertMember adds m, or replaces the existing member with the same Name.
// Matching by Name (the routing key) means a re-spawn under an existing name
// updates rather than duplicates.
func (s *RosterStore) UpsertMember(m RosterMember) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.roster.Members {
		if s.roster.Members[i].Name == m.Name {
			s.roster.Members[i] = m
			s.persistLocked()
			return
		}
	}
	s.roster.Members = append(s.roster.Members, m)
	s.persistLocked()
}

// RemoveMember drops the member with the given Name (e.g. on team_dismiss) so
// a restart does not resurrect a teammate the leader deliberately retired.
// No-op when the name is absent.
func (s *RosterStore) RemoveMember(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.roster.Members {
		if s.roster.Members[i].Name == name {
			s.roster.Members = append(s.roster.Members[:i], s.roster.Members[i+1:]...)
			s.persistLocked()
			return
		}
	}
}

// Snapshot returns a deep copy of the current roster for resume. The Members
// slice is freshly allocated so callers can iterate without holding the lock.
func (s *RosterStore) Snapshot() Roster {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Roster{Team: s.roster.Team, Description: s.roster.Description}
	if len(s.roster.Members) > 0 {
		out.Members = make([]RosterMember, len(s.roster.Members))
		copy(out.Members, s.roster.Members)
	}
	return out
}

// Clear drops the team and all members, removing roster.json from disk. Used
// when the leader deletes the team (team_dismiss of the whole team).
func (s *RosterStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roster = Roster{}
	if s.dir != "" {
		_ = os.Remove(filepath.Join(s.dir, rosterFile))
	}
}

func (s *RosterStore) persistLocked() {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recreate team dir %s: %v\n", s.dir, err)
		return
	}
	data, err := json.MarshalIndent(s.roster, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: marshal roster: %v\n", err)
		return
	}
	// Reuse the package's atomic write helper (defined in task_store.go).
	if err := taskWriteFileAtomic(filepath.Join(s.dir, rosterFile), data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write roster: %v\n", err)
	}
}
