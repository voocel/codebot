package dream

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"

	"github.com/voocel/codebot/internal/config"
)

const (
	// sessionScanInterval throttles the session-gate directory scan: when
	// the time gate passes but the session gate doesn't, the lock mtime
	// never advances, so without this the scan would repeat every turn.
	sessionScanInterval = 10 * time.Minute

	// runTimeout bounds a consolidation run. CC relies on user aborts only;
	// a detached background goroutine needs a hard ceiling.
	runTimeout = 10 * time.Minute
)

// agentRunner is the narrow execution contract the Dreamer needs; tests inject
// a fake.
type agentRunner interface {
	Run(ctx context.Context, agent, task string) (subagent.RunResult, error)
}

// Config wires a Dreamer into the host session. Directories are injected
// (rather than derived from cwd here) so tests never touch the real
// ~/.codebot tree.
type Config struct {
	MemoryDir   string // config.MemoryDir(cwd)
	SessionsDir string // config.SessionsDir(cwd)
	Settings    config.DreamSettings
	// CurrentSession returns the active session ID so the session gate can
	// exclude it (its transcript mtime is always fresh). May be nil.
	CurrentSession func() string
	// TaskRT registers the run so /tasks shows it and can kill it.
	TaskRT *task.Runtime
	// Runner executes the dream agent; a private subagent.Runner so
	// the run never touches the main agent (no notify, no follow-up).
	Runner agentRunner
	// OnDone fires after a run finishes; err is nil on success. Optional.
	OnDone func(err error)
}

// Dreamer triggers background memory consolidation. MaybeStart is the
// idle-hook entry (gated), StartManual the /dream entry (immediate).
// The lock file is the cross-process guard; `running` the in-process one.
type Dreamer struct {
	cfg  Config
	lock *Lock

	mu         sync.Mutex
	running    bool
	lastScanAt time.Time
}

func New(cfg Config) *Dreamer {
	return &Dreamer{cfg: cfg, lock: NewLock(cfg.MemoryDir)}
}

// MaybeStart runs the auto-trigger gates, cheapest first: enabled (no IO),
// in-process running flag, time gate (one stat), scan throttle. The session
// gate does directory IO, so it runs on a goroutine off the event path.
func (d *Dreamer) MaybeStart() {
	if !d.cfg.Settings.Enabled {
		return
	}
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return
	}
	lastAt := d.lock.LastConsolidatedAt()
	if time.Since(lastAt) < time.Duration(d.cfg.Settings.MinHours)*time.Hour {
		d.mu.Unlock()
		return
	}
	if time.Since(d.lastScanAt) < sessionScanInterval {
		d.mu.Unlock()
		return
	}
	d.lastScanAt = time.Now()
	d.mu.Unlock()

	go func() {
		touched := countSessionsTouchedSince(d.cfg.SessionsDir, lastAt, d.currentSession())
		if touched < d.cfg.Settings.MinSessions {
			return
		}
		_, _ = d.acquireAndStart(touched)
	}()
}

// StartManual triggers a consolidation immediately (/dream), skipping the
// time, throttle, and session gates. The lock still applies: it is what
// prevents two concurrent runs across processes.
func (d *Dreamer) StartManual() (taskID string, err error) {
	touched := countSessionsTouchedSince(d.cfg.SessionsDir, d.lock.LastConsolidatedAt(), d.currentSession())
	return d.acquireAndStart(touched)
}

func (d *Dreamer) acquireAndStart(sessionsTouched int) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return "", errors.New("a memory consolidation is already running")
	}
	prior, ok := d.lock.TryAcquire()
	if !ok {
		return "", errors.New("another process is consolidating memory right now")
	}
	d.running = true
	return d.start(prior, sessionsTouched), nil
}

// start registers the task entry and launches the run. Caller holds d.mu
// with running=true; the goroutine clears it on exit.
func (d *Dreamer) start(prior time.Time, sessionsTouched int) string {
	taskID := d.cfg.TaskRT.NextID("dream")
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)

	prompt := buildConsolidationPrompt(d.cfg.MemoryDir, d.cfg.SessionsDir, sessionsTouched)
	entry := &task.Entry{
		ID:          taskID,
		Type:        task.TypeSubAgent,
		Agent:       agentName,
		Description: "memory consolidation",
		Prompt:      prompt,
		Status:      task.Running,
		StartedAt:   time.Now(),
		Depth:       1,
	}
	entry.SetCancel(cancel)
	d.cfg.TaskRT.Register(entry)

	go d.run(ctx, cancel, taskID, prior, prompt)
	return taskID
}

func (d *Dreamer) run(ctx context.Context, cancel context.CancelFunc, taskID string, prior time.Time, prompt string) {
	defer d.cfg.TaskRT.Done(taskID)
	defer cancel()
	defer func() {
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
	}()

	res, err := d.cfg.Runner.Run(ctx, agentName, prompt)

	d.cfg.TaskRT.Update(taskID, func(e *task.Entry) {
		e.EndedAt = time.Now()
		e.TokensIn, e.TokensOut, e.ToolCount = res.Usage.Input, res.Usage.Output, res.Usage.Tools
		switch {
		case err != nil && errors.Is(ctx.Err(), context.Canceled):
			e.Status = task.Killed
			e.Error = "killed"
		case err != nil:
			e.Status = task.Failed
			e.Error = err.Error()
		default:
			e.Status = task.Completed
			e.Result = res.Output
		}
	})

	if err != nil {
		// Failed or killed: rewind the lock so the time gate reopens.
		// The scan throttle is the retry backoff.
		d.lock.Rollback(prior)
	} else {
		d.lock.Complete()
	}
	if d.cfg.OnDone != nil {
		d.cfg.OnDone(err)
	}
}

func (d *Dreamer) currentSession() string {
	if d.cfg.CurrentSession == nil {
		return ""
	}
	return d.cfg.CurrentSession()
}
