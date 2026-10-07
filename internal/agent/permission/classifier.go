package permission

import (
	"encoding/json"
)

// In a classification, path is checked against ReadRoots or WriteRoots by
// capability. For exec, command yields the approval keys (see commandKeys)
// and workdir is checked against WriteRoots. WebFetch(host) rules match url.
type classification struct {
	capability Capability
	path       string
	command    string
	workdir    string
	url        string
	// confirm is why the user must confirm this call each time. The mode and
	// stored approvals don't apply, and an allow covers only this call. Deny
	// rules still apply first.
	confirm string
}

func classify(workspace string, req Request) classification {
	c := classifyTool(req)
	c.confirm = checkDangerousPath(workspace, req)
	return c
}

// classifyTool covers the built-in tools; MCP tools classify themselves
// through Metadata. web_fetch only GETs and web_search only sends a query,
// so both count as reads; a "web_fetch" or WebFetch(host) deny rule blocks
// them. Internal tools change only model-authored state, so there is no
// side effect to gate.
func classifyTool(req Request) classification {
	switch req.ToolName {
	case "read":
		return classification{capability: CapabilityRead, path: pathField(req.Args)}
	case "glob", "grep", "ls":
		return classification{capability: CapabilityRead, path: stringField(req.Args, "path")}
	case "write", "edit":
		return classification{capability: CapabilityWrite, path: pathField(req.Args)}
	case "bash":
		cmd := stringField(req.Args, "command")
		capability := CapabilityExec
		if isReadonlyBash(cmd) {
			capability = CapabilityRead
		}
		return classification{capability: capability, command: cmd, workdir: stringField(req.Args, "workdir")}
	case "web_fetch":
		return classification{capability: CapabilityRead, url: stringField(req.Args, "url")}
	case "web_search":
		return classification{capability: CapabilityRead}
	case "todo_write", "task_output", "task_stop", "subagent", "skill", "ask_user", "tool_search", "enter_worktree", "exit_worktree":
		return classification{capability: CapabilityInternal}
	}
	return classification{}
}

func stringField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func pathField(raw json.RawMessage) string {
	if v := stringField(raw, "file_path"); v != "" {
		return v
	}
	return stringField(raw, "path")
}
