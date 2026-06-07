package goal

import (
	"strings"
	"testing"
)

func TestManagerLifecycle(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil)
	state, err := m.Create("ship goal mode")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if state.Status != StatusActive {
		t.Fatalf("status = %q, want active", state.Status)
	}
	if state.ID == "" {
		t.Fatal("expected generated goal id")
	}

	if _, err := m.Create("another goal"); err == nil {
		t.Fatal("expected duplicate active goal to fail")
	}
	if state, err = m.Pause(); err != nil || state.Status != StatusPaused {
		t.Fatalf("pause = (%q, %v), want paused nil", state.Status, err)
	}
	if state, err = m.Resume(); err != nil || state.Status != StatusActive {
		t.Fatalf("resume = (%q, %v), want active nil", state.Status, err)
	}
	if state, err = m.Complete("done"); err != nil || state.Status != StatusComplete {
		t.Fatalf("complete = (%q, %v), want complete nil", state.Status, err)
	}
}

func TestManagerBlockRequiresReason(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil)
	if _, err := m.Create("ship goal mode"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.Block(""); err == nil {
		t.Fatal("expected empty blocked reason to fail")
	}
}

func TestManagerBlockRequiresThreeMatchingAttempts(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil)
	if _, err := m.Create("ship goal mode"); err != nil {
		t.Fatalf("create: %v", err)
	}

	state, err := m.Block("needs user credentials")
	if err == nil {
		t.Fatal("expected first blocked attempt to fail")
	}
	if state.Status != StatusActive {
		t.Fatalf("status after first attempt = %q, want active", state.Status)
	}
	if state.BlockedCount != 1 {
		t.Fatalf("blocked count after first attempt = %d, want 1", state.BlockedCount)
	}

	state, err = m.Block("needs admin credentials")
	if err == nil {
		t.Fatal("expected changed blocker to restart audit and fail")
	}
	if state.BlockedCount != 1 {
		t.Fatalf("blocked count after changed blocker = %d, want 1", state.BlockedCount)
	}

	state, err = m.Block("needs admin credentials")
	if err == nil {
		t.Fatal("expected second matching blocked attempt to fail")
	}
	if state.BlockedCount != 2 {
		t.Fatalf("blocked count after second matching attempt = %d, want 2", state.BlockedCount)
	}

	state, err = m.Block("needs user credentials")
	if err == nil {
		t.Fatal("expected changed blocker to restart audit and fail")
	}
	if state.BlockedCount != 1 {
		t.Fatalf("blocked count after switching back = %d, want 1", state.BlockedCount)
	}

	state, err = m.Block("needs user credentials")
	if err == nil {
		t.Fatal("expected second matching blocked attempt to fail")
	}

	state, err = m.Block("needs user credentials")
	if err != nil {
		t.Fatalf("third matching blocked attempt: %v", err)
	}
	if state.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked", state.Status)
	}
	if state.Reason != "needs user credentials" {
		t.Fatalf("reason = %q", state.Reason)
	}
}

func TestManagerBlockAuditDoesNotAdvanceWithinSameModelTurn(t *testing.T) {
	t.Parallel()

	totalTokens := 100
	m := NewManager(nil, nil)
	m.SetTokenCounter(func() int { return totalTokens })
	if _, err := m.Create("ship goal mode"); err != nil {
		t.Fatalf("create: %v", err)
	}

	state, err := m.Block("needs user credentials")
	if err == nil {
		t.Fatal("expected first blocked attempt to fail")
	}
	if state.BlockedCount != 1 {
		t.Fatalf("blocked count after first attempt = %d, want 1", state.BlockedCount)
	}

	state, err = m.Block("needs user credentials")
	if err == nil {
		t.Fatal("expected same-turn blocked attempt to fail")
	}
	if state.BlockedCount != 1 {
		t.Fatalf("same-turn blocked count = %d, want 1", state.BlockedCount)
	}

	totalTokens = 140
	state, err = m.Block("needs user credentials")
	if err == nil {
		t.Fatal("expected second model-turn blocked attempt to fail")
	}
	if state.BlockedCount != 2 {
		t.Fatalf("second model-turn blocked count = %d, want 2", state.BlockedCount)
	}

	totalTokens = 180
	state, err = m.Block("needs user credentials")
	if err != nil {
		t.Fatalf("third model-turn blocked attempt: %v", err)
	}
	if state.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked", state.Status)
	}
}

func TestManagerBudgetLimitSignal(t *testing.T) {
	t.Parallel()

	totalTokens := 100
	m := NewManager(nil, nil)
	m.SetTokenCounter(func() int { return totalTokens })
	state, err := m.CreateWithBudget("ship goal mode", 50)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	totalTokens = 160
	sig := m.signal()
	if sig.Err != nil {
		t.Fatalf("signal error: %v", sig.Err)
	}
	if !sig.Active {
		t.Fatal("expected budget-limit signal to be active")
	}
	if sig.Key != "goal_budget:"+state.ID {
		t.Fatalf("signal key = %q", sig.Key)
	}
	if !strings.Contains(sig.Reminder, "reached its token budget") {
		t.Fatalf("budget prompt missing budget notice: %s", sig.Reminder)
	}

	snapshot := m.Snapshot()
	if snapshot.Status != StatusBudgetLimited {
		t.Fatalf("status = %q, want budget_limited", snapshot.Status)
	}
	if snapshot.TokensUsed != 60 {
		t.Fatalf("tokens used = %d, want 60", snapshot.TokensUsed)
	}
	if !snapshot.BudgetLimitReported {
		t.Fatal("expected budget limit to be marked reported")
	}

	sig = m.signal()
	if sig.Err != nil {
		t.Fatalf("second signal error: %v", sig.Err)
	}
	if sig.Active {
		t.Fatalf("expected second budget-limit signal to be inactive, got key %q", sig.Key)
	}
}

func TestManagerBudgetLimitedResumeRequiresHigherBudget(t *testing.T) {
	t.Parallel()

	totalTokens := 100
	m := NewManager(nil, nil)
	m.SetTokenCounter(func() int { return totalTokens })
	if _, err := m.CreateWithBudget("ship goal mode", 50); err != nil {
		t.Fatalf("create: %v", err)
	}

	totalTokens = 160
	if sig := m.signal(); sig.Err != nil || !sig.Active {
		t.Fatalf("budget signal = (%v, %v), want active nil", sig.Active, sig.Err)
	}

	if _, err := m.Resume(); err == nil {
		t.Fatal("expected budget-limited resume without a new budget to fail")
	}
	if _, err := m.ResumeWithBudget(60); err == nil {
		t.Fatal("expected resume with budget equal to tokens used to fail")
	}

	state, err := m.ResumeWithBudget(100)
	if err != nil {
		t.Fatalf("resume with higher budget: %v", err)
	}
	if state.Status != StatusActive {
		t.Fatalf("status = %q, want active", state.Status)
	}
	if state.TokenBudget != 100 {
		t.Fatalf("token budget = %d, want 100", state.TokenBudget)
	}
	if state.BudgetLimitReported {
		t.Fatal("expected resumed goal to clear budget-limit reported marker")
	}

	sig := m.signal()
	if sig.Err != nil {
		t.Fatalf("signal after resume: %v", sig.Err)
	}
	if !sig.Active || sig.Key == "goal_budget:"+state.ID {
		t.Fatalf("signal after resume = (%v, %q), want normal goal continuation", sig.Active, sig.Key)
	}
}
