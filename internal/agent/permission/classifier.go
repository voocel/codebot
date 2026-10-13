package permission

import (
	"encoding/json"
)

// In a classification, path is checked against ReadRoots or WriteRoots by
// capability. A command's workdir is checked against WriteRoots and the
// files its read-only commands read against ReadRoots. WebFetch(host) rules
// match url.
type classification struct {
	capability Capability
	path       string
	command    string
	workdir    string
	reads      []string // see shellLine
	keys       []string // see shellLine
	intent     string   // what the call says it does
	url        string
	// confirm is why the user must confirm this call each time. The mode and
	// stored approvals don't apply, and an allow covers only this call. Deny
	// rules still apply first.
	confirm string
	// unsandboxed is a command that asks to run outside the sandbox.
	unsandboxed bool
}

func classify(workspace string, req Request) classification {
	c := classifyTool(req)
	c.confirm = checkDangerousPath(workspace, req.ToolName, c)
	return c
}

// args are the arguments of the built-in tools that the engine reads,
// decoded as the tools decode theirs, so that both see the same call: a key
// in any case names its field, and of two keys for one field the last wins.
type args struct {
	FilePath    string `json:"file_path"`
	Path        string `json:"path"`
	Command     string `json:"command"`
	WorkDir     string `json:"workdir"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Unsandboxed bool   `json:"unsandboxed"`
}

// classifyTool covers the built-in tools; MCP tools classify themselves
// through Metadata. web_search only sends a query to the search provider, so
// it counts as a read; web_fetch reaches any host, which its URL can carry
// data out to, so it asks for hosts not yet allowed. Internal tools change
// only model-authored state, so there is no side effect to gate.
func classifyTool(req Request) classification {
	var a args
	// A tool fails a call it cannot decode, so whatever a failed decode
	// leaves here never runs.
	_ = json.Unmarshal(req.Args, &a)
	switch req.ToolName {
	case "read":
		return classification{capability: CapabilityRead, path: a.FilePath}
	case "glob", "grep", "ls":
		return classification{capability: CapabilityRead, path: a.Path}
	case "write", "edit":
		return classification{capability: CapabilityWrite, path: a.FilePath}
	case "bash":
		line := readShell(a.Command)
		capability := CapabilityExec
		if line.readonly {
			capability = CapabilityRead
		}
		return classification{capability: capability, command: a.Command, workdir: a.WorkDir, reads: line.reads, keys: line.keys, intent: a.Description, unsandboxed: a.Unsandboxed}
	case "web_fetch":
		return classification{capability: CapabilityNetwork, url: a.URL}
	case "web_search":
		return classification{capability: CapabilityRead}
	case "todo_write", "task_output", "task_stop", "subagent", "skill", "ask_user", "tool_search", "enter_worktree", "exit_worktree":
		return classification{capability: CapabilityInternal}
	}
	return classification{}
}
