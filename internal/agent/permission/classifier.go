package permission

import (
	"encoding/json"
)

// classification is what a tool call does, as the engine weighs it.
//
// Fields by capability:
//   - Read    : path is checked against ReadRoots and becomes the summary.
//   - Write   : path is checked against WriteRoots and becomes the summary.
//   - Exec    : command becomes the summary, and each command it runs an
//     approval key (see commandKeys); workdir, if set, is checked against
//     WriteRoots.
//
// url is what web_fetch fetches; it becomes the summary, which WebFetch(host)
// rules match.
type classification struct {
	capability Capability
	path       string
	command    string
	workdir    string
	url        string
	// confirm, when set, is why the user must confirm this call each time:
	// the mode and stored approvals do not apply, and an allow covers this
	// call only. Deny rules still apply first.
	confirm string
}

// classify maps a tool request, run in workspace, to its capability and
// operand fields, and asks to confirm each call that touches a dangerous
// path (see checkDangerousPath).
func classify(workspace string, req Request) classification {
	c := classifyTool(req)
	c.confirm = checkDangerousPath(workspace, req)
	return c
}

// classifyTool maps a tool request to its capability and operand fields.
// Tools whose request carries Metadata, the MCP tools, classify
// themselves; this covers the rest:
//   - read/glob/grep/ls       → Read  (path checked against ReadRoots)
//   - write/edit              → Write (path checked against WriteRoots)
//   - bash                    → Exec  (command + optional workdir)
//   - web_fetch / web_search  → Read  (no local side effect: web_fetch only
//     GETs, web_search only takes a query; a deny rule on the tool name,
//     such as "web_fetch", turns them off, and WebFetch(host) the fetches
//     of a host)
//   - skill, todo_write, task control, subagent, ask_user,
//     tool_search, worktree   → Internal (state changes authored by the
//     model, with no side effects of their own to gate on)
//
// read/edit/write expose `file_path`; glob/grep/ls expose `path`. We probe
// `file_path` first so the canonical argument wins when both are present.
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

// pathField reads the file path field, preferring file_path (used by
// read/edit/write) and falling back to path (still used by glob/grep/ls).
func pathField(raw json.RawMessage) string {
	if v := stringField(raw, "file_path"); v != "" {
		return v
	}
	return stringField(raw, "path")
}
