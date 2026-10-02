// Package session runs one conversation on an agentcore.Agent. A Session
// keeps the log the history is replayed from, decides when inputs start,
// steer or follow up runs, and publishes the events. It knows nothing about
// the features built on it: everything a run needs arrives in a RunSpec.
//
// A Session is an actor. Every method enqueues an operation that a single
// goroutine applies in order, so no method but Wait blocks on a run. Every
// message and compaction is recorded in the log before it enters the
// Agent's history, so History() always equals what the log replays to.
package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/storage"
)

// ErrClosed is returned by calls on a closed session.
var ErrClosed = errors.New("session closed")

// Source says where an input comes from, which decides when it is processed.
type Source int

const (
	// User input is inserted into a live run at its next tool boundary and
	// starts a run otherwise. It clears a Cancel.
	User Source = iota
	// Background input, such as a finished background task, is processed
	// before a live run ends and starts a run otherwise — except after a
	// Cancel, when it waits for the next User input.
	Background
)

// Input is a message for the conversation.
type Input struct {
	Source Source
	Msg    agentcore.Message
}

// RunSpec describes how the next run is made.
type RunSpec struct {
	// Provider, Model and Effort name the model selection; they are
	// recorded in the log whenever one of them changes.
	Provider string
	Model    string
	Effort   string
	Window   int // context window of the model, reported in Status

	// Config configures the runs; its Compactor, which Compact uses too,
	// must be set. The Agent sets its Emit, Steering and FollowUp.
	Config agentcore.Config
	// WrapRun, when set, is called as each run starts and returns the run's
	// context and a function called with the run's error when it ends.
	WrapRun func(ctx context.Context) (context.Context, func(err error))
}

// Status is a snapshot of the session for display.
type Status struct {
	SessionID string
	Provider  string
	Model     string
	Effort    string
	Window    int
	// Context estimates the tokens of the next request.
	Context int
	Running bool
	// Usage sums every response of the session, including those a
	// compaction replaced.
	Usage   agentcore.Usage
	LastRun *agentcore.RunEnd
}

// Session is a running conversation. See the package documentation.
type Session struct {
	store *storage.Store
	id    string
	agent *agentcore.Agent

	ops    mailbox
	done   chan struct{} // closed when the actor has stopped
	events hub

	statusMu sync.Mutex
	status   Status

	// Actor state, touched only by operations.
	spec        RunSpec
	recorded    storage.Model
	run         *run
	compacting  context.CancelFunc // non-nil while a manual compaction runs
	compactWait *compactRequest    // a Compact waiting for the canceled run to end
	pending     []Input            // inputs waiting for no run to be live
	waiters     []chan<- func()    // Waits to release at the next Idle
	aborted     bool
	closing     bool
	finished    bool
}

// run is the state of the run under way.
type run struct {
	cancel   context.CancelFunc
	canceled bool // a canceled run takes no more input
}

type compactRequest struct {
	ctx   context.Context
	reply chan<- error
}

// Open starts a session on store, whose replayed state is state. When the
// model spec selects differs from the recorded one, the change is recorded.
func Open(store *storage.Store, state storage.State, spec RunSpec) (*Session, error) {
	s := &Session{
		store:    store,
		id:       store.Header().SessionID,
		agent:    agentcore.NewAgent(spec.Config, state.Messages),
		ops:      mailbox{wake: make(chan struct{}, 1)},
		done:     make(chan struct{}),
		spec:     spec,
		recorded: state.Model,
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

// Post queues an input. It never blocks; after Close it does nothing.
func (s *Session) Post(in Input) { s.ops.push(func() { s.post(in) }) }

// Cancel stops the live run or compaction and drops User inputs that have
// not entered the history. Background inputs wait for the next User input.
func (s *Session) Cancel() { s.ops.push(s.cancel) }

// Configure replaces the spec. A live run keeps the spec it started with.
func (s *Session) Configure(spec RunSpec) { s.ops.push(func() { s.configure(spec) }) }

// Compact compacts the history with the spec's Compactor, canceling a live
// run first. The summary is written in the background: the session keeps
// accepting calls, and inputs posted meanwhile run once it is committed.
func (s *Session) Compact(ctx context.Context) error {
	reply := make(chan error, 1)
	if !s.ops.push(func() { s.compact(ctx, reply) }) {
		return ErrClosed
	}
	return <-reply
}

// Query asks the model a one-off question after the current history, sharing
// the conversation's request prefix and prompt cache. Neither the question
// nor the answer enters the history. edit, if set, adjusts the request.
func (s *Session) Query(ctx context.Context, prompt string, edit func(*litellm.Request)) (string, error) {
	var call agentcore.Call
	if !s.do(func() { call = agentcore.BuildCall(s.spec.Config, s.agent.Messages()) }) {
		return "", ErrClosed
	}
	req := call.Request
	req.Messages = append(req.Messages, litellm.UserText(prompt))
	if edit != nil {
		edit(&req)
	}
	resp, err := call.Client.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text()), nil
}

// Wait returns once the session is idle with every input posted before it
// handled, and every subscriber has been handed the events up to that point
// — the Idle event among them, unless nothing ran. It must not be called
// from a subscriber, which would wait for itself.
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

// History returns the history, including what a live run has recorded so
// far.
func (s *Session) History() []agentcore.Message { return s.agent.Messages() }

// Status returns the latest status.
func (s *Session) Status() Status {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

// Subscribe calls fn with every event from now on, in order, on a goroutine
// of its own. A slow subscriber delays only itself.
func (s *Session) Subscribe(fn func(Event)) (unsubscribe func()) {
	return s.events.subscribe(fn)
}

// Close cancels the live run, waits for it to end and closes the log. Events
// already published are still delivered.
func (s *Session) Close() {
	s.ops.push(s.close)
	<-s.done
}

// do runs op on the actor and waits for it; false means the session is closed.
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
		// Cleared first, so the canceled run cannot take the user's input
		// while it winds down.
		r.canceled = true
		_, followUp := s.agent.ClearQueues()
		s.queue(Background, followUp)
		r.cancel()
	}
	if s.compacting != nil {
		s.compacting()
	}
}

// queue adds msgs from source to the pending inputs.
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

// release lets the waiting Waits return once subscribers have caught up.
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
	s.agent.SetConfig(spec.Config)
	if err := s.record(); err != nil {
		s.emit(Event{Kind: Error, Err: err})
	}
	s.refreshStatus()
}

// record logs the spec's model selection when it differs from the last one.
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

// settle decides what follows once a run or compaction has ended.
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

// start runs prompts on the Agent; the run hands control back to the actor
// when it ends.
func (s *Session) start(prompts []agentcore.Message) {
	ctx, cancel := context.WithCancel(context.Background())
	s.run = &run{cancel: cancel}
	s.refreshStatus()
	s.emit(Event{Kind: RunStarted})
	spec := s.spec
	go func() {
		defer cancel()
		end := func(error) {}
		if spec.WrapRun != nil {
			ctx, end = spec.WrapRun(ctx)
		}
		err := s.agent.Prompt(ctx, prompts...)
		end(err)
		s.ops.push(s.runEnded)
	}()
}

func (s *Session) runEnded() {
	s.run = nil
	// Inputs that arrived after the run last asked for them.
	steering, followUp := s.agent.ClearQueues()
	s.queue(User, steering)
	s.queue(Background, followUp)
	s.refreshStatus()
	s.settle()
}

// observe records what enters the history before the Agent applies it, and
// publishes every event. It runs on the goroutine of the run or compaction.
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
			if err := s.store.AppendCompaction(e.Compaction.Messages); err != nil {
				return err
			}
			s.updateStatus(func(st *Status) { st.Context = s.estimate(e.Compaction.Messages) })
		}
	case agentcore.RunEnd:
		s.updateStatus(func(st *Status) { st.LastRun = &e })
	}
	s.emit(Event{Kind: Agent, Agent: ev})
	return nil
}

// estimate estimates the next request with history, by the Agent's Config.
func (s *Session) estimate(history []agentcore.Message) int {
	return agentcore.Estimate(s.agent.Config(), history)
}

// refreshStatus recomputes the status from the actor state and announces it.
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

// mailbox is the actor's unbounded, ordered queue of operations.
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
