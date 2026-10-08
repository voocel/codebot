// Package session runs one conversation on an agentcore.Agent. It knows no
// features; everything a run needs comes in a RunSpec.
//
// A Session is an actor: each method queues an operation for one goroutine,
// so only Wait blocks on a run. Messages and compactions are written to the
// log before they enter the Agent's history, so History always equals a
// replay of the log.
package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/session/storage"
)

var ErrClosed = errors.New("session closed")

// Source decides when an input is processed.
type Source int

const (
	// User input steers a live run at its next tool boundary, or starts a
	// run. It clears a previous Cancel.
	User Source = iota
	// Background input, such as a finished background task, joins a live run
	// before it ends, or starts a run. After a Cancel it waits for the next
	// User input.
	Background
)

type Input struct {
	Source Source
	Msg    agentcore.Message
}

type RunSpec struct {
	// Provider, Model and Effort are logged whenever one of them changes.
	Provider string
	Model    string
	Effort   string
	Window   int

	// Config.Compactor must be set. The Agent fills in Emit, Steering and
	// FollowUp.
	Config agentcore.Config
	// WrapRun returns the run's context and a callback for the run's error.
	WrapRun func(ctx context.Context) (context.Context, func(err error))
	// Context returns messages describing the current environment that the
	// history does not already carry. They are prepended to each run's
	// inputs, and appended to a compacted history, since compaction drops
	// the earlier copies.
	Context func(history []agentcore.Message) []agentcore.Message
	// Checkpoint returns the workspace and its tree as a run begins, or ""
	// for the tree when the workspace isn't checkpointed. It runs on the
	// run's goroutine.
	Checkpoint func() (dir, tree string)
}

type Status struct {
	SessionID string
	Provider  string
	Model     string
	Effort    string
	Window    int
	// Context is the estimated token count of the next request.
	Context int
	Running bool
	// Usage includes responses that a compaction later replaced.
	Usage   agentcore.Usage
	LastRun *agentcore.RunEnd
}

type Session struct {
	store *storage.Store
	id    string
	agent *agentcore.Agent

	ops    mailbox
	done   chan struct{} // closed when the actor has stopped
	events hub

	statusMu sync.Mutex
	status   Status

	// Owned by the actor goroutine.
	spec        RunSpec
	recorded    storage.Model
	run         *run
	compacting  context.CancelFunc // non-nil while a manual compaction runs
	compactWait *compactRequest    // a Compact waiting for the canceled run to end
	pending     []Input            // inputs waiting for the live run to end
	checkpoints []storage.Checkpoint
	waiters     []chan<- func() // Waits to release at the next Idle
	aborted     bool
	closing     bool
	finished    bool
}

type run struct {
	cancel   context.CancelFunc
	canceled bool // a canceled run takes no more input
}

type compactRequest struct {
	ctx   context.Context
	reply chan<- error
}

// Open logs the model selection if spec changes it.
func Open(store *storage.Store, state storage.State, spec RunSpec) (*Session, error) {
	s := &Session{
		store:       store,
		id:          store.Header().SessionID,
		agent:       agentcore.NewAgent(spec.agentConfig(), state.Messages),
		ops:         mailbox{wake: make(chan struct{}, 1)},
		done:        make(chan struct{}),
		spec:        spec,
		recorded:    state.Model,
		checkpoints: state.Checkpoints,
	}
	if err := s.record(); err != nil {
		return nil, err
	}
	s.agent.Subscribe(s.observe)
	s.status = Status{SessionID: s.id, Usage: state.Usage}
	s.refreshStatus()
	go s.loop()
	return s, nil
}

// Post never blocks; after Close it does nothing.
func (s *Session) Post(in Input) { s.ops.push(func() { s.post(in) }) }

// Cancel stops the live run or compaction and drops queued User inputs.
// Queued Background inputs wait for the next User input.
func (s *Session) Cancel() { s.ops.push(s.cancel) }

// Configure leaves a live run on the spec it started with.
func (s *Session) Configure(spec RunSpec) { s.ops.push(func() { s.configure(spec) }) }

// Compact cancels a live run first. The session keeps accepting calls while
// the summary is written; inputs posted meanwhile run after it is committed.
func (s *Session) Compact(ctx context.Context) error {
	reply := make(chan error, 1)
	if !s.ops.push(func() { s.compact(ctx, reply) }) {
		return ErrClosed
	}
	return <-reply
}

// Query asks a one-off question on top of the current history, reusing the
// conversation's prompt cache. Neither the question nor the answer is kept.
func (s *Session) Query(ctx context.Context, prompt string, maxTokens int) (string, error) {
	var call agentcore.Call
	if !s.do(func() {
		history := s.agent.Messages()
		call = agentcore.BuildCall(s.spec.Config, append(history, s.spec.Context(history)...))
	}) {
		return "", ErrClosed
	}
	req := call.Request
	req.Messages = append(req.Messages, litellm.UserText(prompt))
	if maxTokens > 0 {
		req.MaxTokens = &maxTokens
	}
	resp, err := call.Client.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text()), nil
}

// Wait returns once the session is idle, every earlier input is handled and
// every subscriber has received the events up to that point. Calling it from
// a subscriber deadlocks.
func (s *Session) Wait(ctx context.Context) error {
	released := make(chan func(), 1)
	if !s.ops.push(func() { s.wait(released) }) {
		return ErrClosed
	}
	select {
	case flushed := <-released:
		flushed()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Checkpoints returns those of the runs the history holds, oldest first,
// with that history.
func (s *Session) Checkpoints() ([]storage.Checkpoint, []agentcore.Message) {
	var cps []storage.Checkpoint
	var history []agentcore.Message
	s.do(func() { cps, history = slices.Clone(s.checkpoints), s.agent.Messages() })
	return cps, history
}

// ErrBusy refuses an Edit while the agent works.
var ErrBusy = errors.New("session: the agent is working")

// Edit runs fn between runs: no run starts while fn works, so fn may also
// change the workspace the history speaks of. fn changes the history
// through e; what it did before failing stays.
func (s *Session) Edit(fn func(e Editor) error) error {
	err := ErrClosed
	s.do(func() {
		switch {
		case s.closing:
		case s.run != nil || s.compacting != nil:
			err = ErrBusy
		default:
			err = fn(Editor{s})
		}
	})
	return err
}

// Editor changes the history inside Edit.
type Editor struct{ s *Session }

// Holds reports whether the history still holds cp's run, which a
// compaction or an earlier rewind drops.
func (e Editor) Holds(cp storage.Checkpoint) bool { return slices.Contains(e.s.checkpoints, cp) }

// Rewind cuts the history back to before cp's run, which it must hold.
func (e Editor) Rewind(cp storage.Checkpoint) error {
	s := e.s
	if !e.Holds(cp) {
		return errors.New("session: the history no longer holds that run")
	}
	if err := s.store.AppendRewind(cp.At); err != nil {
		return err
	}
	if err := s.agent.SetMessages(s.agent.Messages()[:cp.At]); err != nil {
		return err
	}
	s.checkpoints = slices.DeleteFunc(s.checkpoints, func(c storage.Checkpoint) bool { return c.At >= cp.At })
	s.refreshStatus()
	return nil
}

// Append adds m to the history, such as a note for the next run to read.
func (e Editor) Append(m agentcore.Message) error {
	s := e.s
	if err := s.store.Append(m); err != nil {
		return err
	}
	if err := s.agent.SetMessages(append(s.agent.Messages(), m)); err != nil {
		return err
	}
	s.refreshStatus()
	return nil
}

// History includes what a live run has recorded so far.
func (s *Session) History() []agentcore.Message { return s.agent.Messages() }

func (s *Session) Status() Status {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

// Subscribe delivers events in order on a goroutine per subscriber, so a
// slow subscriber delays only itself.
func (s *Session) Subscribe(fn func(Event)) (unsubscribe func()) {
	return s.events.subscribe(fn)
}

// Close waits for the canceled run to end. Events already published are
// still delivered.
func (s *Session) Close() {
	s.ops.push(s.close)
	<-s.done
}

// do runs op on the actor and waits; false means the session is closed.
func (s *Session) do(op func()) bool {
	done := make(chan struct{})
	if !s.ops.push(func() { op(); close(done) }) {
		return false
	}
	<-done
	return true
}

func (s *Session) loop() {
	defer close(s.done)
	for range s.ops.wake {
		for _, op := range s.ops.take() {
			op()
		}
		if s.finished {
			// The mailbox is closed; run what it accepted before.
			for _, op := range s.ops.take() {
				op()
			}
			return
		}
	}
}

func (s *Session) post(in Input) {
	if s.closing {
		return
	}
	if in.Source == User {
		s.aborted = false
	}
	if r := s.run; r != nil && !r.canceled {
		if in.Source == User {
			s.agent.Steer(in.Msg)
		} else {
			s.agent.FollowUp(in.Msg)
		}
		return
	}
	s.pending = append(s.pending, in)
	s.startPending()
}

func (s *Session) cancel() {
	s.aborted = true
	s.pending = slices.DeleteFunc(s.pending, func(in Input) bool { return in.Source == User })
	if r := s.run; r != nil && !r.canceled {
		// Clear the queues first so the winding-down run can't take user
		// input.
		r.canceled = true
		_, followUp := s.agent.ClearQueues()
		s.queue(Background, followUp)
		r.cancel()
	}
	if s.compacting != nil {
		s.compacting()
	}
}

func (s *Session) queue(source Source, msgs []agentcore.Message) {
	for _, m := range msgs {
		s.pending = append(s.pending, Input{Source: source, Msg: m})
	}
}

func (s *Session) wait(released chan<- func()) {
	if s.run == nil && s.compacting == nil && s.compactWait == nil {
		released <- s.events.mark()
		return
	}
	s.waiters = append(s.waiters, released)
}

func (s *Session) release() {
	if len(s.waiters) == 0 {
		return
	}
	flushed := s.events.mark()
	for _, w := range s.waiters {
		w <- flushed
	}
	s.waiters = nil
}

func (s *Session) configure(spec RunSpec) {
	if s.closing {
		return
	}
	s.spec = spec
	s.agent.SetConfig(spec.agentConfig())
	if err := s.record(); err != nil {
		s.emit(Event{Kind: Error, Err: err})
	}
	s.refreshStatus()
}

func (s *Session) record() error {
	m := storage.Model{Provider: s.spec.Provider, Model: s.spec.Model, Effort: s.spec.Effort}
	if m == s.recorded {
		return nil
	}
	if err := s.store.AppendModel(m); err != nil {
		return err
	}
	s.recorded = m
	return nil
}

func (s *Session) compact(ctx context.Context, reply chan<- error) {
	switch {
	case s.closing:
		reply <- ErrClosed
	case s.compacting != nil || s.compactWait != nil:
		reply <- errors.New("session: compaction already in progress")
	case s.run != nil:
		s.cancel()
		s.compactWait = &compactRequest{ctx: ctx, reply: reply}
	default:
		s.startCompaction(ctx, reply)
	}
}

func (s *Session) startCompaction(ctx context.Context, reply chan<- error) {
	ctx, cancel := context.WithCancel(ctx)
	s.compacting = cancel
	go func() {
		err := s.agent.Compact(ctx)
		cancel()
		s.ops.push(func() { s.compacted(err, reply) })
	}()
}

func (s *Session) compacted(err error, reply chan<- error) {
	s.compacting = nil
	reply <- err
	s.refreshStatus()
	s.settle()
}

func (s *Session) close() {
	if s.closing {
		return
	}
	s.closing = true
	s.cancel()
	s.settle()
}

// settle picks the next step after a run or compaction ends.
func (s *Session) settle() {
	switch {
	case s.run != nil || s.compacting != nil:
	case s.closing:
		s.finish()
	case s.compactWait != nil:
		req := s.compactWait
		s.compactWait = nil
		s.startCompaction(req.ctx, req.reply)
	default:
		s.startPending()
		if s.run == nil {
			s.emit(Event{Kind: Idle})
			s.release()
		}
	}
}

func (s *Session) finish() {
	if req := s.compactWait; req != nil {
		s.compactWait = nil
		req.reply <- ErrClosed
	}
	s.release()
	s.store.Close()
	s.ops.close()
	s.events.close()
	s.finished = true
}

func (s *Session) startPending() {
	if s.run != nil || s.compacting != nil || s.closing || s.aborted || len(s.pending) == 0 {
		return
	}
	prompts := make([]agentcore.Message, len(s.pending))
	for i, in := range s.pending {
		prompts[i] = in.Msg
	}
	s.pending = nil
	s.start(prompts)
}

func (s *Session) start(prompts []agentcore.Message) {
	at := len(s.agent.Messages())
	prompts = append(s.spec.Context(s.agent.Messages()), prompts...)
	ctx, cancel := context.WithCancel(context.Background())
	s.run = &run{cancel: cancel}
	s.refreshStatus()
	s.emit(Event{Kind: RunStarted})
	spec := s.spec
	go func() {
		defer cancel()
		ctx, end := spec.WrapRun(ctx)
		s.checkpoint(spec, at)
		err := s.agent.Prompt(ctx, prompts...)
		end(err)
		s.ops.push(s.runEnded)
	}()
}

// checkpoint records where the run begins: in the history, and, when the
// workspace is checkpointed, in the workspace. It runs on the run's goroutine,
// as checkpointing a large workspace takes a while.
func (s *Session) checkpoint(spec RunSpec, at int) {
	c := storage.Checkpoint{At: at}
	if spec.Checkpoint != nil {
		c.Dir, c.Tree = spec.Checkpoint()
	}
	if err := s.store.AppendCheckpoint(c); err != nil {
		s.emit(Event{Kind: Error, Err: err})
		return
	}
	s.ops.push(func() { s.checkpoints = append(s.checkpoints, c) })
}

func (s *Session) runEnded() {
	s.run = nil
	// Inputs that arrived after the run last drained its queues.
	steering, followUp := s.agent.ClearQueues()
	s.queue(User, steering)
	s.queue(Background, followUp)
	s.refreshStatus()
	s.settle()
}

// observe writes to the log before the Agent updates its history. It runs on
// the run's or compaction's goroutine, not the actor's.
func (s *Session) observe(ev agentcore.Event) error {
	switch e := ev.(type) {
	case agentcore.MessageEnd:
		if err := s.store.Append(e.Message); err != nil {
			return err
		}
		history := append(s.agent.Messages(), e.Message)
		s.updateStatus(func(st *Status) {
			st.Usage.Add(e.Message.Usage)
			st.Context = s.estimate(history)
		})
	case agentcore.CompactionEnd:
		if e.Compaction != nil {
			if err := s.store.AppendCompaction(e.Compaction); err != nil {
				return err
			}
			// The runs they began are gone from the history.
			s.ops.push(func() { s.checkpoints = nil })
			s.updateStatus(func(st *Status) {
				st.Usage.Add(e.Compaction.Usage)
				st.Context = s.estimate(e.Compaction.Messages)
			})
		}
	case agentcore.RunEnd:
		s.updateStatus(func(st *Status) { st.LastRun = &e })
	}
	s.emit(Event{Kind: Agent, Agent: ev})
	return nil
}

func (spec RunSpec) agentConfig() agentcore.Config {
	cfg := spec.Config
	cfg.Compactor = retelling{cfg.Compactor, spec.Context}
	return cfg
}

// retelling appends the context messages to a compacted history, since
// compaction replaced the earlier copies.
type retelling struct {
	agentcore.Compactor
	context func(history []agentcore.Message) []agentcore.Message
}

func (r retelling) Compact(ctx context.Context, history []agentcore.Message, call func([]agentcore.Message) agentcore.Call) (*agentcore.Compaction, error) {
	c, err := r.Compactor.Compact(ctx, history, call)
	if c == nil || err != nil {
		return c, err
	}
	c.Messages = slices.Concat(c.Messages, r.context(c.Messages))
	return c, nil
}

func (s *Session) estimate(history []agentcore.Message) int {
	return agentcore.Estimate(s.agent.Config(), history)
}

func (s *Session) refreshStatus() {
	s.updateStatus(func(st *Status) {
		st.Provider, st.Model = s.spec.Provider, s.spec.Model
		st.Effort, st.Window = s.spec.Effort, s.spec.Window
		st.Running = s.run != nil
		st.Context = s.estimate(s.agent.Messages())
	})
}

func (s *Session) updateStatus(fn func(*Status)) {
	s.statusMu.Lock()
	fn(&s.status)
	s.statusMu.Unlock()
	s.emit(Event{Kind: StatusChanged})
}

func (s *Session) emit(ev Event) { s.events.publish(ev) }

// mailbox is unbounded, so pushing never blocks.
type mailbox struct {
	mu     sync.Mutex
	ops    []func()
	wake   chan struct{}
	closed bool
}

func (m *mailbox) push(op func()) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.ops = append(m.ops, op)
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return true
}

func (m *mailbox) take() []func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	ops := m.ops
	m.ops = nil
	return ops
}

func (m *mailbox) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}
