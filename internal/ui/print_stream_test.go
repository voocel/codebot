package ui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/codebot/internal/agent"
)

type printCountingModel struct {
	calls atomic.Int32
}

func (m *printCountingModel) response() agentcore.Message {
	m.calls.Add(1)
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock("ok")},
		StopReason: agentcore.StopReasonStop,
	}
}

func (m *printCountingModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{Message: m.response()}, nil
}

func (m *printCountingModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	msg := m.response()
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: msg, StopReason: msg.StopReason}
	close(ch)
	return ch, nil
}

func (*printCountingModel) SupportsTools() bool { return true }

func TestWaitForPrintCompletionProcessesBackgroundResult(t *testing.T) {
	model := &printCountingModel{}
	coreAgent := agentcore.NewAgent(agentcore.WithModel(model))
	sess := agent.NewSession(agent.SessionConfig{Agent: coreAgent, ChatModel: model})
	defer sess.Close()

	if err := sess.Prompt("initial"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	sess.WaitForIdle()
	if got := model.calls.Load(); got != 1 {
		t.Fatalf("initial model calls = %d, want 1", got)
	}

	taskRT := task.NewRuntime()
	entry := &task.Entry{ID: "bg-1", Type: task.TypeSubAgent, Status: task.Running}
	taskRT.Register(entry)
	release := make(chan struct{})
	go func() {
		<-release
		taskRT.Update(entry.ID, func(e *task.Entry) { e.Status = task.Completed })
		sess.EnqueueBackgroundResult(agentcore.UserMsg("background result"))
		taskRT.Done(entry.ID)
	}()

	waited := make(chan struct{})
	go func() {
		waitForPrintCompletion(sess, taskRT)
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("print completion returned while a background task was active")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("print completion did not settle")
	}
	if got := model.calls.Load(); got != 2 {
		t.Fatalf("model calls after background result = %d, want 2", got)
	}
	if got := taskRT.Active(); got != 0 {
		t.Fatalf("active tasks = %d, want 0", got)
	}
}
