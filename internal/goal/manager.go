package goal

import (
	"fmt"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/storage"
)

type Manager struct {
	stateAppender StateAppender
	suspend       func() bool
	tokenCounter  func() int
	onChange      func(Change)
	state         *Store
	now           func() time.Time
}

type StateAppender interface {
	AppendGoalState(storage.GoalStateEntry) error
}

type TokenCounter interface {
	TotalTokens() int
}

func NewManager(receiver SignalReceiver, stateAppender StateAppender) *Manager {
	m := &Manager{
		stateAppender: stateAppender,
		state:         NewStore(),
		now:           time.Now,
	}
	if counter, ok := receiver.(TokenCounter); ok {
		m.tokenCounter = counter.TotalTokens
	} else if counter, ok := stateAppender.(TokenCounter); ok {
		m.tokenCounter = counter.TotalTokens
	}
	if receiver != nil {
		receiver.SetGoalSignal(m.signal)
		if changeReceiver, ok := receiver.(ChangeReceiver); ok {
			m.onChange = changeReceiver.HandleGoalChange
		}
	}
	return m
}

func (m *Manager) SetSuspender(fn func() bool) {
	m.suspend = fn
}

func (m *Manager) SetTokenCounter(fn func() int) {
	m.tokenCounter = fn
}

func (m *Manager) SetChangeHandler(fn func(Change)) {
	m.onChange = fn
}

func (m *Manager) Snapshot() State {
	return m.state.Snapshot()
}

func (m *Manager) Restore(state State) error {
	state = state.Normalize()
	if err := ValidateStatus(state.Status); err != nil {
		return err
	}
	if m.tokenCounter != nil {
		state.TokenTotalAtLastAccount = m.tokenCounter()
	}
	m.state.Replace(state)
	return nil
}

func (m *Manager) Create(objective string) (State, error) {
	return m.CreateWithBudget(objective, 0)
}

func (m *Manager) CreateWithBudget(objective string, tokenBudget int) (State, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return State{}, fmt.Errorf("goal objective is required")
	}
	if tokenBudget < 0 {
		return State{}, fmt.Errorf("goal token budget cannot be negative")
	}
	current := m.state.Snapshot().Normalize()
	if current.Status != StatusOff {
		return State{}, fmt.Errorf("goal already %s; use /goal clear before creating a new goal", current.Status)
	}

	now := m.now()
	next := State{
		ID:          storage.GenerateName(),
		Objective:   objective,
		Status:      StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
		TokenBudget: tokenBudget,
	}
	if m.tokenCounter != nil {
		next.TokenTotalAtLastAccount = m.tokenCounter()
	}
	return next, m.replaceAndPersist(next)
}

func (m *Manager) Pause() (State, error) {
	current := m.state.Snapshot().Normalize()
	if current.Status != StatusActive {
		return current, fmt.Errorf("goal is not active")
	}
	current.Status = StatusPaused
	current.UpdatedAt = m.now()
	return current, m.replaceAndPersist(current)
}

func (m *Manager) Resume() (State, error) {
	return m.ResumeWithBudget(0)
}

func (m *Manager) ResumeWithBudget(tokenBudget int) (State, error) {
	if tokenBudget < 0 {
		return State{}, fmt.Errorf("goal token budget cannot be negative")
	}
	current := m.state.Snapshot().Normalize()
	if current.Status != StatusPaused && current.Status != StatusBlocked && current.Status != StatusBudgetLimited && current.Status != StatusUsageLimited {
		return current, fmt.Errorf("goal is not paused, blocked, budget-limited, or usage-limited")
	}
	if current.Status == StatusBudgetLimited && tokenBudget == 0 {
		return current, fmt.Errorf("budget-limited goal requires /goal resume --tokens N with N greater than tokens used (%d)", current.TokensUsed)
	}
	if tokenBudget > 0 {
		if tokenBudget <= current.TokensUsed {
			return current, fmt.Errorf("goal token budget must be greater than tokens used (%d)", current.TokensUsed)
		}
		current.TokenBudget = tokenBudget
	}
	current.Status = StatusActive
	current.Reason = ""
	current.BlockedReason = ""
	current.BlockedCount = 0
	current.BlockedAttemptTokenTotal = 0
	current.BudgetLimitReported = false
	current.BlockedAt = time.Time{}
	current.BudgetLimitedAt = time.Time{}
	current.UsageLimitedAt = time.Time{}
	current.UpdatedAt = m.now()
	if m.tokenCounter != nil {
		current.TokenTotalAtLastAccount = m.tokenCounter()
	}
	return current, m.replaceAndPersist(current)
}

func (m *Manager) Clear() (State, error) {
	current := m.state.Snapshot().Normalize()
	if current.Status == StatusOff {
		return current, nil
	}
	next := State{Status: StatusOff, UpdatedAt: m.now()}
	return next, m.replaceAndPersist(next)
}

func (m *Manager) Complete(reason string) (State, error) {
	current := m.state.Snapshot().Normalize()
	if current.Status != StatusActive && current.Status != StatusPaused && current.Status != StatusBudgetLimited {
		return current, fmt.Errorf("no active goal to complete")
	}
	now := m.now()
	current.Status = StatusComplete
	current.Reason = strings.TrimSpace(reason)
	current.CompletedAt = now
	current.UpdatedAt = now
	return current, m.replaceAndPersist(current)
}

func (m *Manager) Block(reason string) (State, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return State{}, fmt.Errorf("blocked goal requires a reason")
	}
	current := m.state.Snapshot().Normalize()
	if current.Status != StatusActive && current.Status != StatusPaused {
		return current, fmt.Errorf("no active goal to block")
	}
	now := m.now()
	attemptTokenTotal, hasAttemptTokenTotal := m.currentTokenTotal()
	sameAttemptTurn := hasAttemptTokenTotal &&
		current.BlockedCount > 0 &&
		sameBlocker(current.BlockedReason, reason) &&
		current.BlockedAttemptTokenTotal == attemptTokenTotal

	switch {
	case sameBlocker(current.BlockedReason, reason) && !sameAttemptTurn:
		current.BlockedCount++
	case !sameBlocker(current.BlockedReason, reason):
		current.BlockedReason = reason
		current.BlockedCount = 1
	}
	if hasAttemptTokenTotal {
		current.BlockedAttemptTokenTotal = attemptTokenTotal
	}
	current.UpdatedAt = now
	if current.BlockedCount < 3 {
		if err := m.replaceAndPersist(current); err != nil {
			return current, err
		}
		return current, fmt.Errorf("blocked goal requires the same blocker to recur for 3 consecutive goal turns before it can be accepted; current audit is %d/3", current.BlockedCount)
	}
	current.Status = StatusBlocked
	current.Reason = reason
	current.BlockedAt = now
	return current, m.replaceAndPersist(current)
}

func (m *Manager) UsageLimit(reason string) (State, error) {
	current := m.state.Snapshot().Normalize()
	if current.Status != StatusActive && current.Status != StatusBudgetLimited {
		return current, fmt.Errorf("no active or budget-limited goal to usage-limit")
	}
	state, _ := m.accountTokens(current)
	now := m.now()
	state.Status = StatusUsageLimited
	state.Reason = strings.TrimSpace(reason)
	if state.Reason == "" {
		state.Reason = "provider usage limit reached"
	}
	state.UsageLimitedAt = now
	state.UpdatedAt = now
	return state, m.replaceAndPersist(state)
}

// signal is polled by the runtime at serialized turn-end continuation points.
// It is intentionally not a pure snapshot: it accounts token usage and may
// persist budget-limit transitions, so callers must not invoke it concurrently.
func (m *Manager) signal() Signal {
	if m.suspend != nil && m.suspend() {
		return Signal{}
	}
	state := m.state.Snapshot().Normalize()
	if state.Status == StatusBudgetLimited {
		if state.BudgetLimitReported {
			return Signal{}
		}
		state.BudgetLimitReported = true
		state.UpdatedAt = m.now()
		if err := m.replaceAndPersist(state); err != nil {
			return Signal{Err: err}
		}
		return Signal{
			Active:   true,
			Key:      "goal_budget:" + state.ID,
			Reminder: BudgetLimitPrompt(state),
		}
	}
	if !state.Active() {
		return Signal{}
	}
	state, changed := m.accountTokens(state)
	if state.Active() && state.TokenBudget > 0 && state.TokensUsed >= state.TokenBudget {
		now := m.now()
		state.Status = StatusBudgetLimited
		state.Reason = "goal token budget reached"
		state.BudgetLimitedAt = now
		state.BudgetLimitReported = true
		state.UpdatedAt = now
		changed = true
	}
	if changed {
		if err := m.replaceAndPersist(state); err != nil {
			return Signal{Err: err}
		}
	}
	if state.Status == StatusBudgetLimited {
		return Signal{
			Active:   true,
			Key:      "goal_budget:" + state.ID,
			Reminder: BudgetLimitPrompt(state),
		}
	}
	return Signal{
		Active:   true,
		Key:      "goal:" + state.ID,
		Reminder: ContinuationPrompt(state),
	}
}

func (m *Manager) accountTokens(state State) (State, bool) {
	total, ok := m.currentTokenTotal()
	if !ok {
		return state, false
	}
	if state.TokenTotalAtLastAccount == 0 {
		state.TokenTotalAtLastAccount = total
		return state, true
	}
	if total < state.TokenTotalAtLastAccount {
		state.TokenTotalAtLastAccount = total
		return state, true
	}
	if total == state.TokenTotalAtLastAccount {
		return state, false
	}
	state.TokensUsed += total - state.TokenTotalAtLastAccount
	state.TokenTotalAtLastAccount = total
	return state, true
}

func (m *Manager) currentTokenTotal() (int, bool) {
	if m.tokenCounter == nil {
		return 0, false
	}
	total := m.tokenCounter()
	if total < 0 {
		total = 0
	}
	return total, true
}

func sameBlocker(previous, next string) bool {
	return normalizeBlocker(previous) == normalizeBlocker(next)
}

func normalizeBlocker(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func (m *Manager) replaceAndPersist(state State) error {
	state = state.Normalize()
	if err := ValidateStatus(state.Status); err != nil {
		return err
	}
	previous := m.state.Snapshot().Normalize()
	if m.stateAppender == nil {
		m.state.Replace(state)
		m.emitChange(previous, state)
		return nil
	}
	if err := m.stateAppender.AppendGoalState(storage.GoalStateEntry{
		ID:                       state.ID,
		Objective:                state.Objective,
		Status:                   string(state.Status),
		CreatedAt:                state.CreatedAt,
		UpdatedAt:                state.UpdatedAt,
		CompletedAt:              state.CompletedAt,
		BlockedAt:                state.BlockedAt,
		BudgetLimitedAt:          state.BudgetLimitedAt,
		UsageLimitedAt:           state.UsageLimitedAt,
		Reason:                   state.Reason,
		BlockedReason:            state.BlockedReason,
		BlockedCount:             state.BlockedCount,
		BlockedAttemptTokenTotal: state.BlockedAttemptTokenTotal,
		BudgetLimitReported:      state.BudgetLimitReported,
		TokenBudget:              state.TokenBudget,
		TokensUsed:               state.TokensUsed,
		TokenTotalAtLastAccount:  state.TokenTotalAtLastAccount,
	}); err != nil {
		return err
	}
	m.state.Replace(state)
	m.emitChange(previous, state)
	return nil
}

// StateFromEntry converts a persisted goal entry back into runtime state.
// Inverse of the mapping in replaceAndPersist.
func StateFromEntry(entry storage.GoalStateEntry) State {
	return State{
		ID:                       entry.ID,
		Objective:                entry.Objective,
		Status:                   Status(entry.Status),
		CreatedAt:                entry.CreatedAt,
		UpdatedAt:                entry.UpdatedAt,
		CompletedAt:              entry.CompletedAt,
		BlockedAt:                entry.BlockedAt,
		BudgetLimitedAt:          entry.BudgetLimitedAt,
		UsageLimitedAt:           entry.UsageLimitedAt,
		Reason:                   entry.Reason,
		BlockedReason:            entry.BlockedReason,
		BlockedCount:             entry.BlockedCount,
		BlockedAttemptTokenTotal: entry.BlockedAttemptTokenTotal,
		BudgetLimitReported:      entry.BudgetLimitReported,
		TokenBudget:              entry.TokenBudget,
		TokensUsed:               entry.TokensUsed,
		TokenTotalAtLastAccount:  entry.TokenTotalAtLastAccount,
	}.Normalize()
}

func (m *Manager) emitChange(previous, current State) {
	if m.onChange == nil {
		return
	}
	m.onChange(Change{
		Previous: previous.Normalize(),
		Current:  current.Normalize(),
	})
}
