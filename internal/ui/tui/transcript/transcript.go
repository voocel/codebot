package transcript

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
)

// Transcript is the cells of a conversation.
type Transcript struct {
	cells  []Cell
	reply  *Assistant       // the response streaming, until it ends
	calls  map[string]*Tool // calls in flight, by ID
	hidden map[string]bool  // calls the transcript leaves out, by ID
	// now is when events happen; a field so tests can fix it.
	now func() time.Time
}

// New returns an empty transcript.
func New() *Transcript {
	return &Transcript{calls: map[string]*Tool{}, hidden: map[string]bool{}, now: time.Now}
}

// Load returns the transcript of a saved history.
func Load(history []agentcore.Message) *Transcript {
	t := New()
	for _, m := range history {
		switch m.Role {
		case litellm.RoleUser:
			if m.Kind == agentcore.KindSummary {
				t.Append(Note(compacted))
				continue
			}
			t.message(m)
		case litellm.RoleAssistant:
			t.message(m)
		case litellm.RoleTool:
			if r, ok := m.ToolResult(); ok {
				t.end(r.ToolUseID, m.Text(), r.IsError)
			}
		}
	}
	t.interrupt()
	return t
}

const compacted = "Earlier conversation compacted"

// Cells returns the transcript's cells, oldest first.
func (t *Transcript) Cells() []Cell { return t.cells }

// Append adds c at the end.
func (t *Transcript) Append(c Cell) { t.cells = append(t.cells, c) }

// Under adds c under the command line to, after what was added under it
// before rather than after what came since, and returns where c went. With
// to no longer in the transcript, c goes at the end.
func (t *Transcript) Under(to *Prompt, c Cell) int {
	after := to.tail
	if after == nil {
		after = to
	}
	to.tail = c
	i := slices.Index(t.cells, after)
	if i < 0 {
		t.Append(c)
		return len(t.cells) - 1
	}
	t.cells = slices.Insert(t.cells, i+1, c)
	return i + 1
}

// Apply takes an event of a run.
func (t *Transcript) Apply(ev agentcore.Event) {
	switch e := ev.(type) {
	case agentcore.MessageStart:
		t.reply = nil
	case agentcore.MessageDelta:
		switch d := e.Event.(type) {
		case litellm.TextDelta:
			t.streaming().text.WriteString(d.Text)
		case litellm.ReasoningDelta:
			t.streaming().thinking.WriteString(d.Text)
		default:
			return
		}
		t.reply.bump()
	case agentcore.Retry:
		// The response so far is discarded.
		t.drop(t.reply)
		t.reply = nil
	case agentcore.MessageEnd:
		if e.Message.Role != litellm.RoleTool {
			t.message(e.Message)
		}
	case agentcore.ToolStart:
		if c := t.calls[e.Call.ID]; c != nil {
			c.Preview, c.Started = e.Call.Preview, t.now()
		} else if !t.hidden[e.Call.ID] {
			t.call(e.Call, t.now())
		}
	case agentcore.ToolUpdate:
		if c := t.calls[e.Call.ID]; c != nil {
			c.progress(e.Progress)
		}
	case agentcore.ToolEnd:
		t.end(e.Call.ID, e.Result.Text(), e.Result.IsError)
	case agentcore.CompactionEnd:
		switch {
		case e.Err != nil && !errors.Is(e.Err, context.Canceled):
			t.Append(Fail("Compaction failed: " + app.ErrorText(e.Err)))
		case e.Compaction != nil:
			t.Append(Note(compacted))
		}
	case agentcore.RunEnd:
		t.interrupt()
		switch {
		case e.Reason == agentcore.EndAborted || errors.Is(e.Err, context.Canceled):
			t.Append(Note("Interrupted"))
		case e.Err != nil:
			t.Append(Fail(app.ErrorText(e.Err)))
		case e.Reason == agentcore.EndMaxTurns:
			t.Append(Note("Stopped at the turn limit"))
		}
	}
}

// streaming returns the reply streaming, which joins the transcript with its
// first content.
func (t *Transcript) streaming() *Assistant {
	if t.reply == nil {
		t.reply = &Assistant{streaming: true}
		t.Append(t.reply)
	}
	return t.reply
}

// message takes a message entering the history. An assistant's calls join
// with it, in the order it made them, whichever starts first.
func (t *Transcript) message(m agentcore.Message) {
	switch m.Role {
	case litellm.RoleUser:
		text, ok := app.UserText(m)
		if !ok {
			return
		}
		images := 0
		for _, b := range m.Blocks {
			if _, ok := b.(litellm.ImageBlock); ok {
				images++
			}
		}
		t.Append(&Prompt{Text: text, Images: images})
	case litellm.RoleAssistant:
		c := t.reply
		t.reply = nil
		if c == nil {
			c = &Assistant{}
			t.Append(c)
		}
		// The message is final: its content replaces what streamed.
		c.streaming = false
		c.text.Reset()
		c.text.WriteString(m.Text())
		c.thinking.Reset()
		c.thinking.WriteString(m.Reasoning())
		c.bump()
		if c.empty() {
			t.drop(c)
		}
		for _, call := range m.ToolCalls() {
			t.call(agentcore.ToolCall{ID: call.ID, Name: call.Name, Args: []byte(call.Arguments)}, m.Time)
		}
	}
}

func (t *Transcript) call(call agentcore.ToolCall, at time.Time) {
	if app.HiddenToolCall(call.Name, call.Args) {
		t.hidden[call.ID] = true
		return
	}
	c := newTool(call.ID, call.Name, call.Args, call.Preview, at)
	t.calls[call.ID] = c
	t.Append(c)
}

func (t *Transcript) end(id, result string, failed bool) {
	if t.hidden[id] {
		delete(t.hidden, id)
		return
	}
	if c := t.calls[id]; c != nil {
		c.finish(result, failed)
		delete(t.calls, id)
	}
}

// interrupt ends what the run left running.
func (t *Transcript) interrupt() {
	for id, c := range t.calls {
		c.interrupt()
		delete(t.calls, id)
	}
	clear(t.hidden)
	if t.reply != nil {
		t.reply.streaming = false
		t.reply.bump()
		t.reply = nil
	}
}

func (t *Transcript) drop(c Cell) {
	if c == nil {
		return
	}
	for i := len(t.cells) - 1; i >= 0; i-- {
		if t.cells[i] == c {
			t.cells = append(t.cells[:i], t.cells[i+1:]...)
			return
		}
	}
}

// activity describes in a line what the transcript's run is doing.
func (t *Transcript) activity() string {
	for i := len(t.cells) - 1; i >= 0; i-- {
		switch c := t.cells[i].(type) {
		case *Tool:
			v := viewOf(c.Name)
			if arg := v.arg(c.args); arg != "" {
				return v.name(c) + "(" + arg + ")"
			}
			return v.name(c)
		case *Assistant:
			if text := firstLine(c.text.String()); text != "" {
				return text
			}
			return "Thinking"
		}
	}
	return ""
}
