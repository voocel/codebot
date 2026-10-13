package acp

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/app"
	cbsession "github.com/voocel/codebot/internal/session"
)

func (s *Server) onEvent(ev app.Event) {
	sess := s.sessionOf(ev.Conversation)
	if sess == nil {
		return
	}
	switch ev.Kind {
	case app.ModeChanged:
		sess.modeChanged(ev.Mode)
	case app.SessionEvent:
		if ev.Session.Kind == cbsession.Agent {
			sess.onAgentEvent(ev.Session.Agent)
		}
	}
}

func (s *Server) sessionOf(c *app.Conversation) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.conv == c {
			return sess
		}
	}
	return nil
}

func (sess *session) onAgentEvent(ev agentcore.Event) {
	switch e := ev.(type) {
	case agentcore.MessageDelta:
		// Tool argument deltas are not forwarded: the call carries its whole
		// input.
		switch d := e.Event.(type) {
		case litellm.ReasoningDelta:
			sess.send(acp.UpdateAgentThoughtText(d.Text))
		case litellm.TextDelta:
			sess.streamed = true
			sess.send(acp.UpdateAgentMessageText(d.Text))
		}
	case agentcore.MessageEnd:
		sess.streamed = false
	case agentcore.Retry:
		// A client cannot take back what it was sent: say the text starts
		// over.
		if sess.streamed {
			sess.streamed = false
			sess.send(acp.UpdateAgentMessageText("\n\n*(The response broke off and starts over.)*\n\n"))
		}
	case agentcore.ToolStart:
		if app.HiddenToolCall(e.Call.Name, e.Call.Args) {
			return
		}
		label := ""
		if e.Call.Tool != nil {
			label = e.Call.Tool.Label
		}
		sess.send(sess.startCall(e.Call.ID, e.Call.Name, e.Call.Args, label))
	case agentcore.ToolEnd:
		if e.Call.Name == todo.ToolName {
			if !e.Result.IsError {
				sess.send(plan(e.Call.Args))
			}
			return
		}
		if app.HiddenToolCall(e.Call.Name, e.Call.Args) {
			return
		}
		sess.send(result(e.Call.ID, e.Call.Name, e.Result.IsError, e.Result.Text()))
	}
}

// replay sends the conversation so far the way it went live, for a client
// loading the session.
func (sess *session) replay() {
	calls := map[string]litellm.ToolUseBlock{}
	var open []string // calls without a result yet
	for _, m := range sess.conv.History() {
		switch m.Role {
		case litellm.RoleUser:
			text, ok := app.UserText(m)
			if !ok {
				continue // what the harness added
			}
			if text != "" {
				sess.send(acp.UpdateUserMessageText(text))
			}
			for _, b := range m.Blocks {
				if img, ok := b.(litellm.ImageBlock); ok && len(img.Data) > 0 {
					sess.send(acp.UpdateUserMessage(acp.ImageBlock(base64.StdEncoding.EncodeToString(img.Data), img.MIME)))
				}
			}
		case litellm.RoleAssistant:
			for _, b := range m.Blocks {
				switch b := b.(type) {
				case litellm.ReasoningBlock:
					if b.Text != "" {
						sess.send(acp.UpdateAgentThoughtText(b.Text))
					}
				case litellm.TextBlock:
					if b.Text != "" {
						sess.send(acp.UpdateAgentMessageText(b.Text))
					}
				case litellm.ToolUseBlock:
					calls[b.ID] = b
					if args := json.RawMessage(b.Arguments); !app.HiddenToolCall(b.Name, args) {
						sess.send(sess.startCall(b.ID, b.Name, args, ""))
						open = append(open, b.ID)
					}
				}
			}
		case litellm.RoleTool:
			r, ok := m.ToolResult()
			if !ok {
				continue
			}
			call := calls[r.ToolUseID]
			args := json.RawMessage(call.Arguments)
			switch {
			case call.Name == todo.ToolName:
				if !r.IsError {
					sess.send(plan(args))
				}
			case !app.HiddenToolCall(call.Name, args):
				sess.send(result(r.ToolUseID, call.Name, r.IsError, m.Text()))
				open = remove(open, r.ToolUseID)
			}
		}
	}
	// Calls an interruption left without a result.
	for _, id := range open {
		sess.send(acp.UpdateToolCall(acp.ToolCallId(id), acp.WithUpdateStatus(acp.ToolCallStatusFailed)))
	}
}

func remove(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// result ends a call with its output. An edit or a write keeps the diff it
// started with, unless it failed.
func result(id, name string, failed bool, output string) acp.SessionUpdate {
	status := acp.ToolCallStatusCompleted
	if failed {
		status = acp.ToolCallStatusFailed
	}
	opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(status), acp.WithUpdateRawOutput(output)}
	if output != "" && (failed || name != "edit" && name != "write") {
		opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(output))}))
	}
	return acp.UpdateToolCall(acp.ToolCallId(id), opts...)
}

// plan shows the todo list in the client's plan.
func plan(args json.RawMessage) acp.SessionUpdate {
	items, _ := todo.Parse(args) // the call succeeded, so its arguments parse
	entries := make([]acp.PlanEntry, 0, len(items))
	for _, it := range items {
		entries = append(entries, acp.PlanEntry{Content: it.Content, Status: acp.PlanEntryStatus(it.Status), Priority: acp.PlanEntryPriorityMedium})
	}
	return acp.SessionUpdate{Plan: &acp.SessionUpdatePlan{Entries: entries}}
}

func (sess *session) startCall(id, name string, args json.RawMessage, label string) acp.SessionUpdate {
	cwd := sess.conv.Cwd()
	title, locations := describe(name, args, label, cwd)
	return acp.StartToolCall(acp.ToolCallId(id), title,
		acp.WithStartKind(toolKind(name)),
		acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartLocations(locations),
		acp.WithStartContent(diff(name, args, cwd)),
		acp.WithStartRawInput(rawJSON(args)),
	)
}

// describe titles a call and names the file it reads or changes, which a
// client can follow.
func describe(name string, args json.RawMessage, label, cwd string) (string, []acp.ToolCallLocation) {
	var a struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Offset   int    `json:"offset"`
		Command  string `json:"command"`
		Pattern  string `json:"pattern"`
		Query    string `json:"query"`
		URL      string `json:"url"`
		Task     string `json:"task"`
	}
	_ = json.Unmarshal(args, &a)
	file := func(verb string) (string, []acp.ToolCallLocation) {
		path := agentcoretools.ResolvePath(cwd, a.FilePath)
		loc := acp.ToolCallLocation{Path: path}
		if a.Offset > 0 {
			loc.Line = acp.Ptr(a.Offset)
		}
		return verb + " " + shown(cwd, path), []acp.ToolCallLocation{loc}
	}
	switch name {
	case "read":
		return file("Read")
	case "edit":
		return file("Edit")
	case "write":
		return file("Write")
	case "bash":
		return "`" + strings.ReplaceAll(firstLine(a.Command), "`", "\\`") + "`", nil
	case "grep":
		if a.Path != "" {
			return "Search " + a.Pattern + " in " + shown(cwd, agentcoretools.ResolvePath(cwd, a.Path)), nil
		}
		return "Search " + a.Pattern, nil
	case "glob":
		return "Find " + a.Pattern, nil
	case "ls":
		return "List " + shown(cwd, agentcoretools.ResolvePath(cwd, a.Path)), nil
	case "web_search":
		return "Search the web: " + a.Query, nil
	case "web_fetch":
		return "Fetch " + a.URL, nil
	case "subagent":
		return firstLine(a.Task), nil
	}
	if label != "" {
		return label, nil
	}
	return name, nil
}

// diff shows an edit or a write as it was asked for, which the client renders
// natively: the text replaced and its replacement, or the file written.
func diff(name string, args json.RawMessage, cwd string) []acp.ToolCallContent {
	var a struct {
		FilePath  string `json:"file_path"`
		Content   string `json:"content"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if json.Unmarshal(args, &a) != nil || a.FilePath == "" {
		return nil
	}
	path := agentcoretools.ResolvePath(cwd, a.FilePath)
	switch name {
	case "edit":
		return []acp.ToolCallContent{acp.ToolDiffContent(path, a.NewString, a.OldString)}
	case "write":
		return []acp.ToolCallContent{acp.ToolDiffContent(path, a.Content)}
	}
	return nil
}

// shown is path relative to cwd when it is inside.
func shown(cwd, path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func toolKind(name string) acp.ToolKind {
	switch name {
	case "read":
		return acp.ToolKindRead
	case "write", "edit":
		return acp.ToolKindEdit
	case "bash":
		return acp.ToolKindExecute
	case "grep", "glob", "ls":
		return acp.ToolKindSearch
	case "web_search", "web_fetch":
		return acp.ToolKindFetch
	case "subagent":
		return acp.ToolKindThink
	default:
		return acp.ToolKindOther
	}
}

func rawJSON(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return r
}
