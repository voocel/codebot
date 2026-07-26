package agent

import (
	"sync"

	"github.com/voocel/codebot/internal/telemetry"
)

// runState owns per-run bookkeeping: the active telemetry span and the retry
// attempt counter. Both live for exactly one agent run and never co-occur
// with generation logic, so they need none of the session lock.
type runState struct {
	mu           sync.Mutex
	activeRun    *telemetry.Run
	retryAttempt int
}

func (r *runState) beginRun(run *telemetry.Run) (previous *telemetry.Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous = r.activeRun
	r.activeRun = run
	return previous
}

// rollbackRun restores previous only when run is still the active one — a
// concurrent successful begin must not be clobbered by a failed one.
func (r *runState) rollbackRun(run, previous *telemetry.Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activeRun == run {
		r.activeRun = previous
	}
}

// endRun takes the active span (nil when none) and clears it.
func (r *runState) endRun() *telemetry.Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.activeRun
	r.activeRun = nil
	return run
}

func (r *runState) setRetryAttempt(attempt int) {
	r.mu.Lock()
	r.retryAttempt = attempt
	r.mu.Unlock()
}

func (r *runState) takeRetryAttempt() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt := r.retryAttempt
	r.retryAttempt = 0
	return attempt
}
