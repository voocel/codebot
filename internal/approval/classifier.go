package approval

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/voocel/agentcore/permission"
)

// classify maps a codebot tool request to its capability and operand
// fields. The agentcore permission engine consults this via
// EngineConfig.Classifier; the library itself stays harness-agnostic.
//
// Tools fall into five buckets:
//   - read/glob/grep/ls       → Read  (path checked against ReadRoots)
//   - write/edit              → Write (path checked against WriteRoots)
//   - bash                    → Exec  (command + optional workdir)
//   - web_fetch / web_search  → Read  (no local side effect; LLM cannot
//     send arbitrary payloads — web_fetch is GET-only behind Tavily, and
//     web_search only takes a query string. Users who want air-gapped
//     execution can add `deny: WebFetch(*)` rules)
//   - Skill, ask_user, plan/task/cron control, tool_search, subagent
//                             → Internal
//
// read/edit/write expose `file_path`; glob/grep/ls expose `path`. We probe
// `file_path` first so the canonical CC-style argument wins when both are
// present.
func classify(req permission.Request) permission.Classification {
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
	case "ask_user",
		"enter_plan_mode", "exit_plan_mode",
		"task_create", "task_get", "task_update", "task_list", "task_output", "task_stop",
		"cron_create", "cron_delete", "cron_list",
		"tool_search", "subagent", "Skill":
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

// pathField reads the file path field, preferring file_path (CC-style,
// used by read/edit/write) and falling back to path (legacy, still used
// by glob/grep/ls).
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
