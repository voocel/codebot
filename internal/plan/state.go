package plan

import "sync"

type Phase string

const (
	PhaseOff      Phase = "off"
	PhasePlanning Phase = "planning"
	PhaseReview   Phase = "review"
)

type AllowedCommand struct {
	CommandPrefix string
	Description   string
}

type State struct {
	Phase           Phase
	Task            string
	Slug            string
	Title           string
	PreMode         string
	AllowedCommands []AllowedCommand
}

type Store struct {
	mu    sync.RWMutex
	state State
}

func NewStore() *Store {
	return &Store{state: State{Phase: PhaseOff}}
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Store) Replace(state State) {
	s.mu.Lock()
	s.state = cloneState(state)
	s.mu.Unlock()
}

func cloneState(state State) State {
	state.AllowedCommands = append([]AllowedCommand(nil), state.AllowedCommands...)
	return state
}
