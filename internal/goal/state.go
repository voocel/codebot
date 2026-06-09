package goal

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusOff           Status = "off"
	StatusActive        Status = "active"
	StatusPaused        Status = "paused"
	StatusComplete      Status = "complete"
	StatusBlocked       Status = "blocked"
	StatusBudgetLimited Status = "budget_limited"
)

type State struct {
	ID                       string
	Objective                string
	Status                   Status
	CreatedAt                time.Time
	UpdatedAt                time.Time
	CompletedAt              time.Time
	BlockedAt                time.Time
	BudgetLimitedAt          time.Time
	Reason                   string
	BlockedReason            string
	BlockedCount             int
	BlockedAttemptTokenTotal int
	BudgetLimitReported      bool
	TokenBudget              int
	TokensUsed               int
	TokenTotalAtLastAccount  int
}

type Change struct {
	Previous State
	Current  State
}

// Signal is the narrow runtime contract consumed by agent.Session. Keeping it
// in this package lets Manager wire continuation without importing agent.
type Signal struct {
	Active   bool
	Key      string
	Reminder string
	Err      error
}

type SignalReceiver interface {
	SetGoalSignal(func() Signal)
}

type ChangeReceiver interface {
	HandleGoalChange(Change)
}

func (s State) Active() bool {
	return s.Status == StatusActive && strings.TrimSpace(s.Objective) != ""
}

func (s State) Empty() bool {
	return s.Status == "" || s.Status == StatusOff
}

func (s State) Normalize() State {
	if s.Status == "" {
		s.Status = StatusOff
	}
	if strings.TrimSpace(s.Objective) == "" {
		s.Status = StatusOff
		s.ID = ""
	}
	return s
}

func ValidateStatus(status Status) error {
	switch status {
	case StatusOff, StatusActive, StatusPaused, StatusComplete, StatusBlocked, StatusBudgetLimited:
		return nil
	default:
		return fmt.Errorf("invalid goal status %q", status)
	}
}

type Store struct {
	mu    sync.RWMutex
	state State
}

func NewStore() *Store {
	return &Store{state: State{Status: StatusOff}}
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Store) Replace(state State) {
	s.mu.Lock()
	s.state = state.Normalize()
	s.mu.Unlock()
}
