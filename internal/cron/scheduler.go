package cron

import (
	"fmt"
	"os"
	"time"
)

const (
	tickInterval      = time.Second
	lockRetryInterval = 5 * time.Second
)

// SchedulerConfig configures the scheduler.
type SchedulerConfig struct {
	Store          *Store
	SessionID      string // for lock ownership
	RestoreDurable bool   // load durable jobs from disk (only for resumed sessions)
	OnFire         func(prompt string)
	IsBusy         func() bool
}

// Scheduler ticks every second and fires ready jobs from both tracks.
type Scheduler struct {
	cfg     SchedulerConfig
	stopCh  chan struct{}
	stopped chan struct{}
	hasLock bool
}

// NewScheduler creates a scheduler (not yet started).
func NewScheduler(cfg SchedulerConfig) *Scheduler {
	return &Scheduler{
		cfg:     cfg,
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Start begins the scheduling loop in a goroutine.
func (s *Scheduler) Start() {
	go s.run()
}

// Stop signals the scheduler to stop and waits for it to finish.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	<-s.stopped
}

func (s *Scheduler) run() {
	defer close(s.stopped)
	defer s.releaseLock()

	store := s.cfg.Store
	store.StartWriter()
	defer store.StopWriter()

	// Load durable tasks from disk only for resumed sessions.
	if store.ConfigDir() != "" && s.cfg.RestoreDurable {
		if err := store.LoadDurableJobs(); err != nil {
			fmt.Fprintf(os.Stderr, "[cron] load durable jobs: %v\n", err)
		}
		// Try to acquire lock for multi-session coordination.
		if s.cfg.SessionID != "" {
			s.hasLock = store.AcquireLock(s.cfg.SessionID)
		}
		s.handleMissedTasks()
	}

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	// Lock retry ticker (only when restoring durable jobs and we don't have the lock yet).
	var lockRetryTicker *time.Ticker
	if s.cfg.RestoreDurable && !s.hasLock && store.ConfigDir() != "" && s.cfg.SessionID != "" {
		lockRetryTicker = time.NewTicker(lockRetryInterval)
		defer lockRetryTicker.Stop()
	}

	for {
		select {
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			s.tick(now)
		case <-lockRetryChan(lockRetryTicker):
			if !s.hasLock {
				s.hasLock = store.AcquireLock(s.cfg.SessionID)
				if s.hasLock {
					_ = store.LoadDurableJobs()
					s.handleMissedTasks()
					lockRetryTicker.Stop()
					lockRetryTicker = nil
				}
			}
		}
	}
}

func lockRetryChan(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func (s *Scheduler) tick(now time.Time) {
	if s.cfg.IsBusy != nil && s.cfg.IsBusy() {
		return
	}

	// Always tick session jobs.
	for _, prompt := range s.cfg.Store.TickSessionJobs(now) {
		s.fire(prompt)
	}

	// Always tick durable jobs — the current session owns all jobs it created,
	// and lock only guards loading external durable jobs from disk.
	for _, prompt := range s.cfg.Store.TickDurableJobs(now) {
		s.fire(prompt)
	}
}

func (s *Scheduler) fire(prompt string) {
	if s.cfg.OnFire != nil {
		s.cfg.OnFire(prompt)
	}
}

func (s *Scheduler) releaseLock() {
	if s.hasLock && s.cfg.SessionID != "" {
		s.cfg.Store.ReleaseLock(s.cfg.SessionID)
		s.hasLock = false
	}
}

func (s *Scheduler) handleMissedTasks() {
	missed := s.cfg.Store.MissedDurableJobs()
	if len(missed) == 0 {
		return
	}

	// Fire a notification prompt about missed tasks.
	notification := BuildMissedNotification(missed)
	s.fire(notification)

	// Remove them from the durable store.
	ids := make([]string, len(missed))
	for i, j := range missed {
		ids[i] = j.ID
	}
	s.cfg.Store.DeleteDurableByIDs(ids)
}
