package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/litellm"
)

// A restored history renders as it did live: each call over its result, a
// read as a summary, and nothing of what only the harness wrote.
func TestRestoreRendersLikeLive(t *testing.T) {
	m := testModel("test-model")
	notification := agentcore.UserText("<task-notification>done</task-notification>")
	notification.Kind = task.KindNotification
	reminder := agentcore.UserText("<system-reminder>context</system-reminder>")
	reminder.Kind = "reminder"
	m.restored = []agentcore.Message{
		reminder,
		agentcore.UserText("look around"),
		{Role: litellm.RoleAssistant, Blocks: []litellm.Block{
			litellm.ToolUseBlock{ID: "a", Name: "bash", Arguments: `{"command":"echo one"}`},
			litellm.ToolUseBlock{ID: "b", Name: "read", Arguments: `{"file_path":"main.go"}`},
		}},
		agentcore.ToolResult("a", agentcore.TextResult(`{"output":"one-output"}`)),
		agentcore.ToolResult("b", agentcore.TextResult("package main\nfunc main() {}")),
		notification,
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	out := ansi.Strip(strings.Join(m.Scrollback, "\n"))
	bash, output, read := strings.Index(out, "echo one"), strings.Index(out, "one-output"), strings.Index(out, "main.go")
	if bash < 0 || output < bash || read < output {
		t.Fatalf("want each call over its result, got:\n%s", out)
	}
	if !strings.Contains(out, "Read 2 lines") || strings.Contains(out, "func main") {
		t.Fatalf("want the read summarized, got:\n%s", out)
	}
	if !strings.Contains(out, "look around") || strings.Contains(out, "task-notification") || strings.Contains(out, "context") {
		t.Fatalf("want the harness's message left out, got:\n%s", out)
	}
}
