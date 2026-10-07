package transcript

import (
	"context"
	"errors"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
)

type Transcript struct {
	cells  []Cell
	reply  *Assistant       // streaming response, nil when none
	calls  map[string]*Tool // calls in flight, by ID
	hidden map[string]bool  // calls not shown, by ID
	// now is a field so tests can fix the time.
	now func() time.Time
}

func New() *Transcript {
	return &Transcript{calls: map[string]*Tool{}, hidden: map[string]bool{}, now: time.Now}
}

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

func (t *Transcript) Cells() []Cell { return t.cells }

func (t *Transcript) Append(c Cell) { t.cells = append(t.cells, c) }

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
		// Discard the response so far.
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

func (t *Transcript) streaming() *Assistant {
	if t.reply == nil {
		t.reply = &Assistant{streaming: true}
		t.Append(t.reply)
	}
	return t.reply
}

// message adds an assistant's calls in the order it made them, whichever
// starts first.
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
		// The final message replaces what streamed.
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
