package approval

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
)

type ToolMetadata struct {
	ToolName    string
	Capability  Capability
	SummaryHint string
	Reason      string
	KeyPrefix   string
}

type toolInfo struct {
	tool       string
	capability Capability
	summary    string
	key        string
	reason     string
	preview    string
}

func inspectCall(workspace string, req agentcore.ToolApprovalRequest) toolInfo {
	call := req.Call
	payload := decodeArgs(call.Args)
	info := toolInfo{
		tool:       call.Name,
		capability: CapUnknown,
		summary:    strings.TrimSpace(req.Summary),
		preview:    previewText(req.Preview),
	}
	if info.summary == "" {
		info.summary = call.Name
	}

	switch call.Name {
	case "read", "glob", "grep", "ls":
		info.capability = CapRead
		info.key = "read"
		if path := pathArg(workspace, payload); path != "" {
			info.summary = path
		}
	case "write", "edit":
		info.capability = CapWrite
		path := pathArg(workspace, payload)
		info.summary = firstNonEmpty(path, info.summary)
		info.key = "write:" + path
		info.reason = "file modification requires approval"
	case "bash":
		info.capability = CapExec
		command := strings.TrimSpace(stringArg(payload, "command"))
		info.summary = firstNonEmpty(command, info.summary)
		info.key = "exec:" + shortHash(command)
		info.reason = "shell execution requires approval"
	case "web_fetch":
		info.capability = CapNetwork
		targetURL := strings.TrimSpace(stringArg(payload, "url"))
		host := hostOf(targetURL)
		info.summary = firstNonEmpty(targetURL, info.summary)
		info.key = "network:" + host
		info.reason = "network access requires approval"
	case "web_search":
		info.capability = CapNetwork
		query := strings.TrimSpace(stringArg(payload, "query"))
		info.summary = firstNonEmpty(query, info.summary)
		info.key = "network:search"
		info.reason = "network access requires approval"
	case "subagent":
		info.capability = CapSubagent
		kind := firstNonEmpty(
			strings.TrimSpace(stringArg(payload, "agent_type")),
			strings.TrimSpace(stringArg(payload, "subagent_type")),
			"default",
		)
		info.summary = kind
		info.key = "subagent:" + kind
		info.reason = "delegating to a sub-agent requires approval"
	case "ask_user", "enter_plan_mode", "exit_plan_mode", "task_create", "task_get", "task_update", "task_list", "cron_create", "cron_list", "cron_remove":
		info.capability = CapInternal
		info.key = "internal:" + call.Name
	default:
		info.capability = CapUnknown
		info.key = "tool:" + call.Name
		info.reason = "unclassified tool requires approval"
	}

	return info
}

func (e *Engine) inspectCall(req agentcore.ToolApprovalRequest) toolInfo {
	info := inspectCall(e.workspace, req)

	e.mu.RLock()
	meta, ok := e.toolMeta[req.Call.Name]
	e.mu.RUnlock()
	if !ok {
		return info
	}
	if meta.Capability != "" {
		info.capability = meta.Capability
	}
	if meta.SummaryHint != "" && strings.TrimSpace(info.summary) == req.Call.Name {
		info.summary = meta.SummaryHint
	}
	if meta.Reason != "" {
		info.reason = meta.Reason
	}
	if meta.KeyPrefix != "" {
		info.key = meta.KeyPrefix + ":" + req.Call.Name
	}
	return info
}

func inspectHook(req HookRequest) toolInfo {
	event := strings.TrimSpace(req.Event)
	command := strings.TrimSpace(req.Command)
	toolName := strings.TrimSpace(req.Tool)
	summary := command
	if event != "" {
		summary = strings.TrimSpace(event + " -> " + command)
	}
	if toolName != "" {
		summary = strings.TrimSpace(event + " (" + toolName + ") -> " + command)
	}
	reason := "hook command requires approval"
	if req.Blocking {
		reason = "blocking hook command requires approval"
	}
	return toolInfo{
		tool:       "hook/" + strings.ToLower(firstNonEmpty(event, "unknown")),
		capability: CapHook,
		summary:    summary,
		key:        "hook:" + strings.ToLower(firstNonEmpty(event, "unknown")) + ":" + shortHash(command),
		reason:     reason,
		preview:    command,
	}
}

func inspectCommand(req CommandRequest) toolInfo {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = "/" + name
	}
	return toolInfo{
		tool:       "command/" + name,
		capability: CapInternal,
		summary:    summary,
		key:        "command:" + string(normalizeCommandCategory(req.Category)) + ":" + name,
		preview:    truncate(strings.TrimSpace(req.Preview), 400),
	}
}

func NormalizeCommandCategory(raw string) CommandCategory {
	return normalizeCommandCategory(CommandCategory(strings.ToLower(strings.TrimSpace(raw))))
}

func normalizeCommandCategory(category CommandCategory) CommandCategory {
	switch category {
	case CommandCategoryInfo, CommandCategorySession, CommandCategoryConfig, CommandCategoryPlan, CommandCategoryExit:
		return category
	default:
		return CommandCategoryPrompt
	}
}

func decodeArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	return payload
}

func stringArg(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func pathArg(workspace string, payload map[string]any) string {
	path := strings.TrimSpace(stringArg(payload, "path"))
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if workspace == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workspace, path))
}

func hostOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "unknown"
	}
	if parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	return "unknown"
}

func previewText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return truncate(text, 400)
	}
	return truncate(string(raw), 400)
}

func truncate(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
