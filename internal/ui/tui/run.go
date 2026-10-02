package tui

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
)

// UI asks the user through dialogs. It is the interact.UI the App boots
// with; Run binds it to the screen.
type UI struct {
	program atomic.Pointer[tea.Program]
}

var _ interact.UI = (*UI)(nil)

func (u *UI) send(msg tea.Msg) { u.program.Load().Send(msg) }

// Approve shows a permission card and waits for the answer.
func (u *UI) Approve(ctx context.Context, req interact.Approval) (interact.Choice, error) {
	resp := make(chan interact.Choice, 1)
	u.send(PermissionMsg{Approval: req, RespCh: resp})
	select {
	case choice := <-resp:
		return choice, nil
	case <-ctx.Done():
		u.send(PermissionDismissMsg{RespCh: resp})
		return interact.Deny, ctx.Err()
	}
}

// Ask shows the questions and waits for the answers. A dialog torn down
// without an answer counts as dismissed.
func (u *UI) Ask(ctx context.Context, qs []interact.Question) (interact.Answers, error) {
	resp := make(chan interact.Answers, 1)
	u.send(AskUserMsg{Questions: qs, RespCh: resp})
	select {
	case answers, ok := <-resp:
		if !ok {
			return interact.Answers{Cancelled: true}, nil
		}
		return answers, nil
	case <-ctx.Done():
		u.send(AskUserDismissMsg{RespCh: resp})
		return interact.Answers{}, ctx.Err()
	}
}

// Run shows a's conversations until the user quits. ui must be the UI a was
// booted with.
func Run(a *app.App, ui *UI, cmds Commands, version string) error {
	m := New(a, cmds, version)
	p := tea.NewProgram(m)
	ui.program.Store(p)

	// The App publishes some events on the caller's goroutine, which may be
	// the program's own; queue them so publishing never waits for Update.
	q := newMsgQueue(p)
	defer q.close()
	unsubscribe := a.Subscribe(func(ev app.Event) { forward(a, q, ev) })
	defer unsubscribe()

	go func() {
		report := a.ConnectMCP(context.Background())
		if report.Servers > 0 {
			p.Send(MCPReadyMsg{Tools: report.Tools, Errors: report.Errors})
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	return nil
}

// forward turns an App event into the TUI's message for it.
func forward(a *app.App, q *msgQueue, ev app.Event) {
	switch ev.Kind {
	case app.Opened:
		q.push(OpenedMsg{Conversation: ev.Conversation})
	case app.ModeChanged:
		q.push(ModeMsg{Mode: ev.Mode})
	case app.SessionEvent:
		switch ev.Session.Kind {
		case session.Agent:
			q.push(AgentEventMsg{Event: ev.Session.Agent})
		case session.RunStarted:
			q.push(RunStartedMsg{})
		case session.StatusChanged:
			q.push(StatusChangedMsg{Status: a.Current().Status()})
		case session.Idle:
			q.push(IdleMsg{})
			go suggest(a.Current(), q)
		case session.Error:
			q.push(CommandResultMsg{Text: ErrorStyle.Render("Session error: " + ev.Session.Err.Error())})
		}
	}
}

// suggest predicts the user's next input after a run.
func suggest(conv *app.Conversation, q *msgQueue) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if text, err := conv.Suggest(ctx); err == nil && text != "" {
		q.push(SuggestionMsg{Text: text})
	}
}

// msgQueue sends messages to the program in order without making the sender
// wait.
type msgQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	msgs   []tea.Msg
	closed bool
}

func newMsgQueue(p *tea.Program) *msgQueue {
	q := &msgQueue{}
	q.cond = sync.NewCond(&q.mu)
	go func() {
		for {
			q.mu.Lock()
			for len(q.msgs) == 0 && !q.closed {
				q.cond.Wait()
			}
			if q.closed {
				q.mu.Unlock()
				return
			}
			batch := q.msgs
			q.msgs = nil
			q.mu.Unlock()
			for _, msg := range batch {
				p.Send(msg)
			}
		}
	}()
	return q
}

func (q *msgQueue) push(msg tea.Msg) {
	q.mu.Lock()
	q.msgs = append(q.msgs, msg)
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *msgQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Signal()
}
