package approval

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/voocel/codebot/internal/permission"
)

// classify maps a codebot tool request, run in workspace, to its capability
// and operand fields, and asks to confirm each call that touches a dangerous
// path (see checkDangerousPath).
func classify(workspace string, req permission.Request) permission.Classification {
	c := classifyTool(req)
	c.Confirm = checkDangerousPath(workspace, req)
	return c
}

// classifyTool maps a tool request to its capability and operand fields.
// Tools whose request carries permission.Metadata, the MCP tools, classify
// themselves; this covers the rest:
//   - read/glob/grep/ls       → Read  (path checked against ReadRoots)
//   - write/edit              → Write (path checked against WriteRoots)
//   - bash                    → Exec  (command + optional workdir)
//   - web_fetch / web_search  → Read  (no local side effect: web_fetch only
//     GETs, web_search only takes a query; a deny rule on the tool name,
//     such as "web_fetch", turns them off)
//   - skill, todo_write, task control, subagent, ask_user,
//     tool_search, worktree   → Internal (state changes authored by the
//     model, with no side effects of their own to gate on)
//
// read/edit/write expose `file_path`; glob/grep/ls expose `path`. We probe
// `file_path` first so the canonical argument wins when both are present.
func classifyTool(req permission.Request) permission.Classification {
	switch req.ToolName {
	case "read":
		return permission.Classification{
			Capability: permission.CapabilityRead,
			Path:       pathField(req.Args),
		}
	case "glob", "grep", "ls":
		return permission.Classification{
			Capability: permission.CapabilityRead,
			Path:       stringField(req.Args, "path"),
		}
	case "write", "edit":
		return permission.Classification{
			Capability: permission.CapabilityWrite,
			Path:       pathField(req.Args),
		}
	case "bash":
		cmd := stringField(req.Args, "command")
		capability := permission.CapabilityExec
		keyPrefix := "exec:"
		if isReadonlyBash(cmd) {
			capability = permission.CapabilityRead
			keyPrefix = "exec:readonly:"
		}
		return permission.Classification{
			Capability: capability,
			Command:    cmd,
			Workdir:    stringField(req.Args, "workdir"),
			Key:        keyPrefix + bashPrefix(cmd),
		}
	case "web_fetch":
		target := stringField(req.Args, "url")
		return permission.Classification{
			Capability: permission.CapabilityRead,
			URL:        target,
			Key:        "web_fetch:" + hostOf(target),
		}
	case "web_search":
		return permission.Classification{
			Capability: permission.CapabilityRead,
			Key:        "web_search",
		}
	case "todo_write", "task_output", "task_stop", "subagent", "skill", "ask_user", "tool_search", "enter_worktree", "exit_worktree":
		return permission.Classification{Capability: permission.CapabilityInternal}
	}
	return permission.Classification{}
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

// hostOf returns a lower-cased hostname for store-key bucketing. Returns
// "unknown" when the raw string is empty or unparseable — keeps the store
// key stable instead of leaking a malformed URL into it.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return strings.ToLower(u.Host)
	}
	return "unknown"
}
