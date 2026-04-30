package approval

import (
	"encoding/json"

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
//   - web_fetch / web_search  → Network
//   - Skill, ask_user, plan/task/cron control, tool_search, subagent
//                             → Internal
func classify(req permission.Request) permission.Classification {
	switch req.ToolName {
	case "read", "glob", "grep", "ls":
		return permission.Classification{
			Capability: permission.CapabilityRead,
			Path:       stringField(req.Args, "path"),
		}
	case "write", "edit":
		return permission.Classification{
			Capability: permission.CapabilityWrite,
			Path:       stringField(req.Args, "path"),
		}
	case "bash":
		return permission.Classification{
			Capability: permission.CapabilityExec,
			Command:    stringField(req.Args, "command"),
			Workdir:    stringField(req.Args, "workdir"),
		}
	case "web_fetch":
		return permission.Classification{
			Capability: permission.CapabilityNetwork,
			URL:        stringField(req.Args, "url"),
		}
	case "web_search":
		return permission.Classification{
			Capability: permission.CapabilityNetwork,
			Key:        "network:search",
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
