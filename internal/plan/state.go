package plan

import "sync"

type Phase string

const (
	PhaseOff      Phase = "off"
	PhasePlanning Phase = "planning"
)

// AllowedPrompt is a semantic description of an action the user pre-approved
// during plan exit (e.g. {Tool: "Bash", Prompt: "run tests"}). Unlike a
// command-prefix whitelist, prompts are surfaced to the user during ask
// dialogs as reference labels — they do NOT auto-allow tool calls.
type AllowedPrompt struct {
	Tool   string
	Prompt string
}

type State struct {
	Phase   Phase
	Slug    string
	PreMode string
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
	return s.state
}

func (s *Store) Replace(state State) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}
