// Package tui is codebot's terminal interface: a full-screen view of the
// conversation over an editor. See docs/tui-plan.md.
package tui

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// UI asks the user through panels. It is the interact.UI the App boots with;
// Run binds it to the screen.
type UI struct {
	program atomic.Pointer[tea.Program]
}

var _ interact.UI = (*UI)(nil)

func (u *UI) send(msg tea.Msg) { u.program.Load().Send(msg) }

// Approve shows a permission request and waits for the answer.
func (u *UI) Approve(ctx context.Context, req interact.Approval) (interact.Verdict, error) {
	reply := make(chan interact.Verdict, 1)
	// The panel answers on the send side, which is how it is known.
	answer := (chan<- interact.Verdict)(reply)
	u.send(approveMsg{req, answer})
	select {
	case v := <-reply:
		return v, nil
	case <-ctx.Done():
		u.send(withdrawMsg{answer})
		return interact.Verdict{Choice: interact.Deny}, ctx.Err()
	}
}

// Ask shows the questions and waits for the answers.
func (u *UI) Ask(ctx context.Context, qs []interact.Question) (interact.Answers, error) {
	reply := make(chan interact.Answers, 1)
	answer := (chan<- interact.Answers)(reply)
	u.send(askMsg{qs, answer})
	select {
	case a := <-reply:
		return a, nil
	case <-ctx.Done():
		u.send(withdrawMsg{answer})
		return interact.Answers{}, ctx.Err()
	}
}

// Run shows a's conversations until the user quits, then leaves the
// conversation in the terminal. ui must be the UI a was booted with.
func Run(a *app.App, ui *UI, version string) error {
	defer logTo(filepath.Join(config.UserConfigDir(), "codebot.log"))()
	theme.Detect()
	m := newModel(a, version)
	p := tea.NewProgram(m)
	ui.program.Store(p)

	// The App publishes some events on the caller's goroutine, which may be
	// the program's own; queue them so publishing never waits for Update.
	q := newQueue(p)
	defer q.close()
	unsubscribe := a.Subscribe(func(ev app.Event) {
		if msg := message(a, ev); msg != nil {
			q.push(msg)
		}
		if ev.Kind == app.SessionEvent && ev.Session.Kind == session.Idle {
			go suggest(a.Current(), q)
		}
	})
	defer unsubscribe()

	go func() { q.push(connectedMsg{a.Connect(context.Background())}) }()

	_, err := p.Run()
	if m.shell != nil {
		m.shell.cancel()
	}
	if err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	m.goodbye()
	return nil
}

// logTo sends the standard logger, which writes to the terminal the TUI
// draws on, to the file at path until restore; nowhere when it cannot be
// opened.
func logTo(path string) (restore func()) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.SetOutput(io.Discard)
		return func() { log.SetOutput(os.Stderr) }
	}
	log.SetOutput(f)
	return func() {
		log.SetOutput(os.Stderr)
		f.Close()
	}
}

// goodbye prints the conversation to the terminal the TUI leaves.
func (m *Model) goodbye() {
	if m.width == 0 || len(m.t.Cells()) == 0 {
		return
	}
	lines := renderAll(m.t.Cells(), m.width, false)
	fmt.Fprintln(os.Stdout, strings.Join(lines, "\n"))
	if len(m.conv.History()) > 0 {
		fmt.Fprintln(os.Stdout, "\n "+theme.SubtleText.Render("Resume this conversation with ")+theme.AccentText.Render("codebot -c"))
	}
}

// message returns the TUI's message for an App event, nil for none.
func message(a *app.App, ev app.Event) tea.Msg {
	switch ev.Kind {
	case app.Opened:
		return openedMsg{ev.Conversation}
	case app.ModeChanged:
		return modeMsg{ev.Mode}
	case app.Reloaded:
		return reloadedMsg{}
	case app.SessionEvent:
		switch ev.Session.Kind {
		case session.Agent:
			return agentMsg{ev.Session.Agent}
		case session.RunStarted:
			return runStartedMsg{}
		case session.StatusChanged:
			return statusMsg{a.Current().Status()}
		case session.Idle:
			return idleMsg{}
		case session.Error:
			return sessionErrMsg{ev.Session.Err}
		}
	}
	return nil
}

// suggest predicts what the user may type next.
func suggest(conv *app.Conversation, q *queue) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if text, err := conv.Suggest(ctx); err == nil && text != "" {
		q.push(suggestionMsg{conv, text})
	}
}

// queue sends messages to the program in order without making the sender
// wait.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	msgs   []tea.Msg
	closed bool
}

func newQueue(p *tea.Program) *queue {
	q := &queue{}
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

func (q *queue) push(msg tea.Msg) {
	q.mu.Lock()
	q.msgs = append(q.msgs, msg)
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Signal()
}
