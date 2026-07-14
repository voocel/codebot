package dream

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"

	"github.com/voocel/codebot/internal/config"
)

// fakeRunner returns run's result, or blocks until ctx cancellation when
// block is set (to exercise kill).
type fakeRunner struct {
	block  bool
	err    error
	output string
	calls  int
}

func (f *fakeRunner) Run(ctx context.Context, _, _ string) (subagent.RunResult, error) {
	f.calls++
	if f.block {
		<-ctx.Done()
		return subagent.RunResult{}, ctx.Err()
	}
	return subagent.RunResult{Output: f.output}, f.err
}

func newTestDreamer(t *testing.T, runner agentRunner, settings config.DreamSettings) (*Dreamer, *task.Runtime, chan error) {
	t.Helper()
	done := make(chan error, 4)
	rt := task.NewRuntime()
	d := New(Config{
		MemoryDir:   t.TempDir(),
		SessionsDir: t.TempDir(),
		Settings:    settings,
		TaskRT:      rt,
		Runner:      runner,
		OnDone:      func(err error) { done <- err },
	})
	return d, rt, done
}

func defaultSettings() config.DreamSettings {
	return config.DreamSettings{Enabled: true, MinHours: 24, MinSessions: 5}
}

func waitDone(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("dream run did not finish")
		return nil
	}
}

func taskByID(t *testing.T, rt *task.Runtime, id string) *task.Entry {
	t.Helper()
	e := rt.Get(id)
	if e == nil {
		t.Fatalf("task %s not registered", id)
	}
	return e
}

func TestManualRunSuccess(t *testing.T) {
	runner := &fakeRunner{output: "merged 3 memories"}
	d, rt, done := newTestDreamer(t, runner, defaultSettings())

	id, err := d.StartManual()
	if err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, done); err != nil {
		t.Fatalf("OnDone err = %v", err)
	}

	e := taskByID(t, rt, id)
	if e.Status != task.Completed || e.Result != "merged 3 memories" {
		t.Fatalf("entry = %+v", e)
	}
	// Complete() cleared the PID body: the holder is gone, mtime remains.
	raw, err := os.ReadFile(d.lock.path())
	if err != nil || len(raw) != 0 {
		t.Fatalf("lock body = %q err=%v, want empty file", raw, err)
	}
	// A second manual run may start immediately after completion.
	if _, err := d.StartManual(); err != nil {
		t.Fatalf("re-run after success blocked: %v", err)
	}
}

func TestManualRunFailureRollsBack(t *testing.T) {
	runner := &fakeRunner{err: errors.New("model exploded")}
	d, rt, done := newTestDreamer(t, runner, defaultSettings())

	id, err := d.StartManual()
	if err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, done); err == nil {
		t.Fatal("OnDone should carry the failure")
	}

	e := taskByID(t, rt, id)
	if e.Status != task.Failed || e.Error != "model exploded" {
		t.Fatalf("entry = %+v", e)
	}
	// prior was zero (no previous consolidation) → rollback removes the file.
	if got := d.lock.LastConsolidatedAt(); !got.IsZero() {
		t.Fatalf("lock mtime = %v, want removed", got)
	}
}

func TestKillViaTaskRuntime(t *testing.T) {
	runner := &fakeRunner{block: true}
	d, rt, done := newTestDreamer(t, runner, defaultSettings())

	id, err := d.StartManual()
	if err != nil {
		t.Fatal(err)
	}
	if !rt.Stop(id) {
		t.Fatal("Stop returned false for a running task")
	}
	if err := waitDone(t, done); err == nil {
		t.Fatal("killed run should report an error")
	}

	e := taskByID(t, rt, id)
	if e.Status != task.Killed {
		t.Fatalf("status = %s, want killed", e.Status)
	}
	if got := d.lock.LastConsolidatedAt(); !got.IsZero() {
		t.Fatalf("lock mtime = %v, want rolled back to absent", got)
	}
}

func TestManualRejectsConcurrentRun(t *testing.T) {
	runner := &fakeRunner{block: true}
	d, rt, done := newTestDreamer(t, runner, defaultSettings())

	id, err := d.StartManual()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartManual(); err == nil {
		t.Fatal("second StartManual should fail while running")
	}
	rt.Stop(id)
	waitDone(t, done)
}

func TestMaybeStartGates(t *testing.T) {
	seedSessions := func(d *Dreamer, n int) {
		for i := range n {
			touch(t, d.cfg.SessionsDir, time.Now().Format("2006-01-02")+"_sess"+string(rune('a'+i))+".jsonl", time.Now())
		}
	}

	t.Run("disabled", func(t *testing.T) {
		s := defaultSettings()
		s.Enabled = false
		runner := &fakeRunner{}
		d, _, _ := newTestDreamer(t, runner, s)
		seedSessions(d, 6)
		d.MaybeStart()
		time.Sleep(50 * time.Millisecond)
		if runner.calls != 0 {
			t.Fatal("disabled dreamer ran")
		}
	})

	t.Run("time gate", func(t *testing.T) {
		runner := &fakeRunner{}
		d, _, _ := newTestDreamer(t, runner, defaultSettings())
		seedSessions(d, 6)
		if _, ok := d.lock.TryAcquire(); !ok { // fresh mtime = consolidated now
			t.Fatal("seed acquire failed")
		}
		d.lock.Complete()
		d.MaybeStart()
		time.Sleep(50 * time.Millisecond)
		if runner.calls != 0 {
			t.Fatal("time gate did not hold")
		}
	})

	t.Run("session gate then scan throttle", func(t *testing.T) {
		runner := &fakeRunner{}
		d, _, _ := newTestDreamer(t, runner, defaultSettings())
		seedSessions(d, 2) // below MinSessions=5
		d.MaybeStart()
		time.Sleep(50 * time.Millisecond)
		if runner.calls != 0 {
			t.Fatal("session gate did not hold")
		}
		// Now enough sessions exist, but the scan just happened → throttled.
		seedSessions(d, 6)
		d.MaybeStart()
		time.Sleep(50 * time.Millisecond)
		if runner.calls != 0 {
			t.Fatal("scan throttle did not hold")
		}
	})

	t.Run("all gates pass", func(t *testing.T) {
		runner := &fakeRunner{output: "ok"}
		d, _, done := newTestDreamer(t, runner, defaultSettings())
		seedSessions(d, 6)
		d.MaybeStart()
		if err := waitDone(t, done); err != nil {
			t.Fatalf("run failed: %v", err)
		}
		if runner.calls != 1 {
			t.Fatalf("runner calls = %d, want 1", runner.calls)
		}
	})

	t.Run("current session excluded", func(t *testing.T) {
		runner := &fakeRunner{}
		d, _, _ := newTestDreamer(t, runner, defaultSettings())
		d.cfg.CurrentSession = func() string { return "self1234" }
		// 5 files but one belongs to the current session → 4 < MinSessions.
		seedSessions(d, 4)
		touch(t, d.cfg.SessionsDir, "2026-07-14_self1234.jsonl", time.Now())
		d.MaybeStart()
		time.Sleep(50 * time.Millisecond)
		if runner.calls != 0 {
			t.Fatal("current session was not excluded")
		}
	})
}
