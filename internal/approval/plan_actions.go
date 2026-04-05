package approval

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore/permission"
	agenttools "github.com/voocel/agentcore/tools"
)

func (e *Engine) SetPlanAllowedCommands(prefixes []string) {
	e.mu.Lock()
	e.planAllow = append([]string(nil), prefixes...)
	e.mu.Unlock()
}

func (e *Engine) decideApprovedPlanAction(req permission.Request) *permission.Decision {
	mode, planMode := e.Mode(), e.PlanMode()
	if planMode || req.ToolName != "bash" {
		return nil
	}

	command, workdir := decodeBashArgs(req.Args)
	matched := e.matchApprovedPlanCommand(command)
	if matched == "" || !e.planWorkdirAllowed(workdir) || e.deniedByRule(req.ToolName, command) {
		return nil
	}

	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = firstNonEmpty(command, "bash")
	}
	info := toolInfo{
		tool:       "bash",
		capability: permission.CapabilityExec,
		summary:    summary,
		preview:    command,
		key:        "plan:bash:" + matched,
	}
	decision := &permission.Decision{
		Kind:       permission.DecisionAllow,
		Source:     permission.DecisionSourceRule,
		Reason:     "allowed by approved plan",
		Capability: permission.CapabilityExec,
		Summary:    summary,
		Key:        info.key,
		Preview:    command,
	}
	e.audit(info, mode, planMode, string(decision.Kind), true, decision.Reason)
	return decision
}

func (e *Engine) deniedByRule(toolName, command string) bool {
	if e.rules == nil {
		return false
	}
	for _, rule := range e.rules.Deny {
		switch rule.Kind {
		case "tool":
			if matchToolName(rule.Pattern, toolName) {
				return true
			}
		case "Bash":
			if toolName == "bash" && matchBash(rule.Pattern, command, true) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) matchApprovedPlanCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" || hasShellOperator(command) {
		return ""
	}

	e.mu.RLock()
	prefixes := append([]string(nil), e.planAllow...)
	e.mu.RUnlock()

	for _, prefix := range prefixes {
		if matchSimpleBash(prefix, command) {
			return prefix
		}
	}
	return ""
}

func (e *Engine) planWorkdirAllowed(workdir string) bool {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return true
	}

	resolved := filepath.Clean(agenttools.ResolvePath(e.cwd, workdir))
	roots := e.tool.FilesystemRoots().WriteRoots
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if withinRoot(resolved, root) {
			return true
		}
	}
	return false
}

func decodeBashArgs(raw json.RawMessage) (command, workdir string) {
	if len(raw) == 0 {
		return "", ""
	}
	var payload struct {
		Command string `json:"command"`
		WorkDir string `json:"workdir"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", ""
	}
	return strings.TrimSpace(payload.Command), strings.TrimSpace(payload.WorkDir)
}

func matchBash(pattern, command string, isDeny bool) bool {
	command = strings.TrimSpace(command)
	pattern = strings.TrimSpace(pattern)
	if command == "" || pattern == "" {
		return false
	}
	if hasShellOperator(command) {
		if !isDeny {
			return false
		}
		for _, seg := range splitShellSegments(command) {
			seg = strings.TrimSpace(seg)
			if seg != "" && matchSimpleBash(pattern, seg) {
				return true
			}
		}
		return false
	}
	return matchSimpleBash(pattern, command)
}

func matchSimpleBash(pattern, command string) bool {
	pt := strings.Fields(strings.TrimSpace(pattern))
	ct := strings.Fields(strings.TrimSpace(command))
	if len(pt) == 0 || len(ct) == 0 {
		return false
	}
	for i, token := range pt {
		if token == "*" {
			return i < len(ct)
		}
		if i >= len(ct) || token != ct[i] {
			return false
		}
	}
	return len(ct) >= len(pt)
}

func matchToolName(pattern, name string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	name = strings.ToLower(strings.TrimSpace(name))
	if pattern == "" || name == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return pattern == name
}

func hasShellOperator(cmd string) bool {
	return len(splitShellSegments(cmd)) > 1
}

func splitShellSegments(cmd string) []string {
	var (
		parts    []string
		buf      strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)
	flush := func() {
		part := strings.TrimSpace(buf.String())
		if part != "" {
			parts = append(parts, part)
		}
		buf.Reset()
	}
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			buf.WriteByte(ch)
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			buf.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			buf.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble {
			if ch == ';' {
				flush()
				continue
			}
			if i+1 < len(cmd) && ((ch == '&' && cmd[i+1] == '&') || (ch == '|' && cmd[i+1] == '|')) {
				flush()
				i++
				continue
			}
			if ch == '|' {
				flush()
				continue
			}
		}
		buf.WriteByte(ch)
	}
	flush()
	return parts
}

func withinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
