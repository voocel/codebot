package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
)

// Profile controls how strict the permission policy is.
type Profile string

const (
	ProfileOff      Profile = "off"
	ProfileBalanced Profile = "balanced"
	ProfileStrict   Profile = "strict"
)

// Config configures the policy engine.
type Config struct {
	Profile     Profile
	Workspace   string
	Interactive bool
	AuditPath   string
}

// Engine validates tool calls before execution.
type Engine struct {
	profile     Profile
	workspace   string
	interactive bool
	auditPath   string
	mu          sync.Mutex
}

// New creates a policy engine.
func New(cfg Config) *Engine {
	p := cfg.Profile
	if p == "" {
		p = ProfileBalanced
	}
	return &Engine{
		profile:     p,
		workspace:   filepath.Clean(cfg.Workspace),
		interactive: cfg.Interactive,
		auditPath:   strings.TrimSpace(cfg.AuditPath),
	}
}

// Permission validates one tool call.
func (e *Engine) Permission(_ context.Context, call agentcore.ToolCall) error {
	err := e.check(call)
	e.audit(call, err)
	return err
}

func (e *Engine) check(call agentcore.ToolCall) error {
	if e.profile == ProfileOff {
		return nil
	}

	switch call.Name {
	case "read", "write", "edit":
		path := extractPath(call.Args, "path")
		if path == "" {
			return fmt.Errorf("policy: %s requires path", call.Name)
		}
		return e.allowPath(path)

	case "find", "grep", "ls":
		path := extractPath(call.Args, "path")
		if path == "" {
			return nil
		}
		return e.allowPath(path)

	case "bash":
		cmd := extractPath(call.Args, "command")
		if cmd == "" {
			return fmt.Errorf("policy: bash requires command")
		}

		// Strict: deny bash by default.
		if e.profile == ProfileStrict {
			return fmt.Errorf("policy: bash denied in strict profile")
		}

		// Balanced + non-interactive: deny bash.
		if !e.interactive {
			return fmt.Errorf("policy: bash denied in non-interactive mode")
		}

		// Balanced + interactive: deny obviously destructive commands.
		if isDangerousCommand(cmd) {
			return fmt.Errorf("policy: dangerous command denied")
		}
		return nil

	default:
		if e.profile == ProfileStrict {
			return fmt.Errorf("policy: tool %q denied in strict profile", call.Name)
		}
		return nil
	}
}

func (e *Engine) audit(call agentcore.ToolCall, decisionErr error) {
	if e.auditPath == "" {
		return
	}

	entry := map[string]any{
		"time":    time.Now().Format(time.RFC3339Nano),
		"profile": string(e.profile),
		"tool":    call.Name,
		"args":    summarizeArgs(call.Args),
		"allow":   decisionErr == nil,
	}
	if decisionErr != nil {
		entry["reason"] = decisionErr.Error()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(e.auditPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(e.auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

func (e *Engine) allowPath(path string) error {
	if e.workspace == "" {
		return nil
	}

	p := filepath.Clean(path)
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.workspace, p)
		p = filepath.Clean(p)
	}

	if !isSubPath(e.workspace, p) {
		return fmt.Errorf("policy: path outside workspace denied: %s", path)
	}
	return nil
}

func isSubPath(base, target string) bool {
	if base == "" {
		return true
	}
	base = filepath.Clean(base)
	target = filepath.Clean(target)

	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func extractPath(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	val, ok := obj[key]
	if !ok {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func summarizeArgs(raw json.RawMessage) map[string]any {
	result := map[string]any{}
	if len(raw) == 0 {
		return result
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return result
	}
	for _, k := range []string{"path", "command", "timeout", "pattern", "glob"} {
		v, ok := obj[k]
		if !ok {
			continue
		}
		switch vv := v.(type) {
		case string:
			if len(vv) > 200 {
				vv = vv[:200] + "..."
			}
			result[k] = vv
		default:
			result[k] = vv
		}
	}
	return result
}

func isDangerousCommand(cmd string) bool {
	s := strings.TrimSpace(strings.ToLower(cmd))
	if s == "" {
		return false
	}

	for _, seg := range splitShellSegments(s) {
		if isDangerousSegment(seg) {
			return true
		}
	}

	return false
}

func isDangerousSegment(seg string) bool {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return false
	}

	// Block direct piping to shell interpreters (e.g. curl ... | sh).
	if pipesToShell(seg) {
		return true
	}

	fields := commandFields(seg)
	if len(fields) == 0 {
		return false
	}

	base := commandBase(fields[0])
	rest := fields[1:]

	switch base {
	case "sudo":
		return true
	case "shutdown", "reboot", "halt", "poweroff":
		return true
	case "rm":
		return isDangerousRM(rest)
	case "dd":
		return ddWritesRawDevice(rest)
	default:
		if strings.HasPrefix(base, "mkfs") {
			return mkfsTargetsRawDevice(rest)
		}
	}

	// Block explicit redirection into raw block devices.
	return writesToRawDevice(seg)
}

func splitShellSegments(s string) []string {
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

	for i := 0; i < len(s); i++ {
		ch := s[i]

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
			if ch == ';' || ch == '\n' {
				flush()
				continue
			}
			if i+1 < len(s) && ((ch == '&' && s[i+1] == '&') || (ch == '|' && s[i+1] == '|')) {
				flush()
				i++
				continue
			}
		}

		buf.WriteByte(ch)
	}
	flush()

	return parts
}

func commandFields(seg string) []string {
	fields := strings.Fields(seg)
	// Skip leading env assignments: FOO=bar CMD ...
	for len(fields) > 0 && isEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	return fields
}

func isEnvAssignment(token string) bool {
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	name := token[:eq]
	for i := 0; i < len(name); i++ {
		c := name[i]
		if i == 0 {
			if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
				return false
			}
			continue
		}
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func commandBase(raw string) string {
	raw = strings.Trim(raw, `"'`)
	return strings.ToLower(path.Base(raw))
}

func isDangerousRM(args []string) bool {
	recursive := false
	force := false
	targets := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		a := strings.Trim(args[i], `"'`)
		if a == "" {
			continue
		}

		if a == "--" {
			for j := i + 1; j < len(args); j++ {
				targets = append(targets, strings.Trim(args[j], `"'`))
			}
			break
		}

		if strings.HasPrefix(a, "--") {
			switch a {
			case "--recursive":
				recursive = true
			case "--force":
				force = true
			}
			continue
		}

		if strings.HasPrefix(a, "-") {
			if strings.ContainsAny(a, "rR") {
				recursive = true
			}
			if strings.Contains(a, "f") {
				force = true
			}
			continue
		}

		targets = append(targets, a)
	}

	if !(recursive && force) {
		return false
	}

	for _, t := range targets {
		switch strings.TrimSpace(t) {
		case "/", "/*", "/.", "/..":
			return true
		}
	}
	return false
}

func pipesToShell(seg string) bool {
	parts := splitByPipe(seg)
	if len(parts) < 2 {
		return false
	}

	for i := 1; i < len(parts); i++ {
		fields := commandFields(parts[i])
		if len(fields) == 0 {
			continue
		}
		base := commandBase(fields[0])
		if isShellBinary(base) {
			return true
		}
		if base == "sudo" && len(fields) > 1 {
			nextBase := commandBase(fields[1])
			if isShellBinary(nextBase) {
				return true
			}
		}
	}
	return false
}

func splitByPipe(s string) []string {
	var (
		parts    []string
		buf      strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)

	flush := func() {
		parts = append(parts, strings.TrimSpace(buf.String()))
		buf.Reset()
	}

	for i := 0; i < len(s); i++ {
		ch := s[i]

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

		if !inSingle && !inDouble && ch == '|' {
			// skip logical OR "||"
			if i+1 < len(s) && s[i+1] == '|' {
				buf.WriteString("||")
				i++
				continue
			}
			flush()
			continue
		}

		buf.WriteByte(ch)
	}
	flush()

	return parts
}

func isShellBinary(base string) bool {
	switch base {
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		return true
	default:
		return false
	}
}

func ddWritesRawDevice(args []string) bool {
	for _, a := range args {
		t := strings.Trim(a, `"'`)
		if !strings.HasPrefix(t, "of=") {
			continue
		}
		target := strings.TrimSpace(strings.TrimPrefix(t, "of="))
		if isRawDevicePath(target) {
			return true
		}
	}
	return false
}

func mkfsTargetsRawDevice(args []string) bool {
	for _, a := range args {
		t := strings.Trim(a, `"'`)
		if isRawDevicePath(t) {
			return true
		}
	}
	return false
}

var rawDeviceRe = regexp.MustCompile(`^/dev/(sd[a-z]\d*|nvme\d+n\d+(p\d+)?|vd[a-z]\d*|xvd[a-z]\d*|mmcblk\d+(p\d+)?)$`)

func isRawDevicePath(p string) bool {
	p = strings.TrimSpace(p)
	return rawDeviceRe.MatchString(p)
}

var redirRawDeviceRe = regexp.MustCompile(`(?:^|[^\w/])(?:>|>>|1>|1>>)\s*/dev/(sd[a-z]\d*|nvme\d+n\d+(p\d+)?|vd[a-z]\d*|xvd[a-z]\d*|mmcblk\d+(p\d+)?)`)

func writesToRawDevice(seg string) bool {
	return redirRawDeviceRe.MatchString(seg)
}
