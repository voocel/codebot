package storage

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestClaim_RejectsAlreadyClaimedByOther(t *testing.T) {
	s := NewTaskStore()
	task := s.Create("x", "y", "", nil)
	if _, err := s.Claim(task.ID, "alice"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.Claim(task.ID, "bob"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("second claim err = %v, want ErrAlreadyClaimed", err)
	}
}

func TestClaim_IdempotentForSameOwner(t *testing.T) {
	s := NewTaskStore()
	task := s.Create("x", "y", "", nil)
	got1, err := s.Claim(task.ID, "alice")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	got2, err := s.Claim(task.ID, "alice")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if got1.Owner != "alice" || got2.Owner != "alice" {
		t.Fatalf("owner not stable: %q / %q", got1.Owner, got2.Owner)
	}
}

func TestClaim_RejectsCompleted(t *testing.T) {
	s := NewTaskStore()
	task := s.Create("x", "y", "", nil)
	done := TaskCompleted
	if _, err := s.Update(task.ID, TaskUpdateOpts{Status: &done}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := s.Claim(task.ID, "alice"); !errors.Is(err, ErrAlreadyResolved) {
		t.Fatalf("err = %v, want ErrAlreadyResolved", err)
	}
}

func TestClaim_RejectsBlocked(t *testing.T) {
	s := NewTaskStore()
	a := s.Create("a", "", "", nil)
	b := s.Create("b", "", "", nil)
	if _, err := s.Update(b.ID, TaskUpdateOpts{AddBlockedBy: []string{a.ID}}); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := s.Claim(b.ID, "alice"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	// Unblock by completing a, then b is claimable.
	done := TaskCompleted
	if _, err := s.Update(a.ID, TaskUpdateOpts{Status: &done}); err != nil {
		t.Fatalf("complete a: %v", err)
	}
	if _, err := s.Claim(b.ID, "alice"); err != nil {
		t.Fatalf("post-unblock claim: %v", err)
	}
}

func TestClaim_NotFound(t *testing.T) {
	s := NewTaskStore()
	if _, err := s.Claim("999", "alice"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
}

// TestClaim_RaceExactlyOneWinner is the CAS contract test: N goroutines racing
// for the same unowned task — exactly one Claim must succeed; the rest see
// ErrAlreadyClaimed. Without the in-Lock check this would silently produce
// "last write wins" and break pull-mode safety.
func TestClaim_RaceExactlyOneWinner(t *testing.T) {
	s := NewTaskStore()
	task := s.Create("x", "y", "", nil)
	const racers = 32

	var (
		wg      sync.WaitGroup
		wins    int32
		startCh = make(chan struct{})
	)
	wg.Add(racers)
	for i := range racers {
		owner := fmt.Sprintf("racer-%d", i) // unique per racer; no idempotent path
		go func() {
			defer wg.Done()
			<-startCh
			if _, err := s.Claim(task.ID, owner); err == nil {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	close(startCh)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1", wins)
	}
}

func TestFindClaimable_PicksFirstUnownedUnblocked(t *testing.T) {
	s := NewTaskStore()
	a := s.Create("a", "", "", nil) // claimed
	b := s.Create("b", "", "", nil) // unowned, blocked by a
	c := s.Create("c", "", "", nil) // unowned, free → should win
	if _, err := s.Claim(a.ID, "alice"); err != nil {
		t.Fatalf("claim a: %v", err)
	}
	if _, err := s.Update(b.ID, TaskUpdateOpts{AddBlockedBy: []string{a.ID}}); err != nil {
		t.Fatalf("link b: %v", err)
	}
	got := s.FindClaimable()
	if got == nil {
		t.Fatal("FindClaimable returned nil, want c")
	}
	if got.ID != c.ID {
		t.Fatalf("picked #%s, want #%s", got.ID, c.ID)
	}
}

func TestFindClaimable_NilWhenAllOwnedOrDone(t *testing.T) {
	s := NewTaskStore()
	a := s.Create("a", "", "", nil)
	b := s.Create("b", "", "", nil)
	if _, err := s.Claim(a.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	done := TaskCompleted
	if _, err := s.Update(b.ID, TaskUpdateOpts{Status: &done}); err != nil {
		t.Fatal(err)
	}
	if got := s.FindClaimable(); got != nil {
		t.Fatalf("FindClaimable = #%s, want nil", got.ID)
	}
}
