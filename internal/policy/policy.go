package policy

import (
	"context"
	"encoding/json"
	"fmt"
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

// AuditEntry is the data passed to an optional audit hook.
type AuditEntry struct {
	Time    time.Time
	Profile Profile
	Tool    string
	Args    map[string]any
	Allow   bool
	Reason  string // empty when allowed
}

// PermitDecision is the user's response to an interactive permission prompt.
type PermitDecision int

const (
	PermitDeny         PermitDecision = iota
	PermitAllowOnce                   // allow this invocation only
	PermitAllowSession                // allow for the rest of the session
	PermitAllowAlways                 // persist to project config
)

// PermitRequest describes the operation awaiting user approval.
type PermitRequest struct {
	Tool    string // e.g. "bash"
	Command string // full command text
	Reason  string // e.g. "dangerous command: sudo"
}

// ConfirmFunc blocks until the user makes a permission decision.
type ConfirmFunc func(ctx context.Context, req PermitRequest) (PermitDecision, error)

// Config configures the policy engine.
type Config struct {
	Profile         Profile
	Workspace       string
	Interactive     bool
	OnAudit         func(AuditEntry) // nil = no auditing
	AllowedCommands []string         // pre-loaded from project config
}

// Engine validates tool calls before execution.
type Engine struct {
	profile     Profile
	workspace   string
	interactive bool
	onAudit     func(AuditEntry)
	confirmFn   ConfirmFunc
	persistFn   func(cmd string) error // persists "always allow" to project config
	mu          sync.RWMutex
	allowed     map[string]struct{} // session-scoped allow-list (key = base command)
	persisted   map[string]struct{} // project-level allow-list (loaded at init)
}

// New creates a policy engine.
func New(cfg Config) *Engine {
	p := cfg.Profile
	if p == "" {
		p = ProfileBalanced
	}
	ws := resolveSymlinks(filepath.Clean(cfg.Workspace))
	persisted := make(map[string]struct{}, len(cfg.AllowedCommands))
	for _, cmd := range cfg.AllowedCommands {
		persisted[cmd] = struct{}{}
	}
	return &Engine{
		profile:     p,
		workspace:   ws,
		interactive: cfg.Interactive,
		onAudit:     cfg.OnAudit,
		persisted:   persisted,
	}
}

// SetConfirmFn sets the interactive confirmation callback.
// Called by the TUI layer after the tea.Program is created.
func (e *Engine) SetConfirmFn(fn ConfirmFunc) {
	e.mu.Lock()
	e.confirmFn = fn
	e.mu.Unlock()
}

// SetPersistFn sets the callback to persist "always allow" commands to project config.
func (e *Engine) SetPersistFn(fn func(cmd string) error) {
	e.mu.Lock()
	e.persistFn = fn
	e.mu.Unlock()
}

func (e *Engine) sessionAllowed(key string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.allowed == nil {
		return false
	}
	_, ok := e.allowed[key]
	return ok
}

func (e *Engine) addSessionAllow(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.allowed == nil {
		e.allowed = make(map[string]struct{})
	}
	e.allowed[key] = struct{}{}
}

// Permission validates one tool call.
func (e *Engine) Permission(ctx context.Context, call agentcore.ToolCall) error {
	err := e.check(ctx, call)
	if e.onAudit != nil {
		entry := AuditEntry{
			Time:    time.Now(),
			Profile: e.profile,
			Tool:    call.Name,
			Args:    summarizeArgs(call.Args),
			Allow:   err == nil,
		}
		if err != nil {
			entry.Reason = err.Error()
		}
		e.onAudit(entry)
	}
	return err
}

func (e *Engine) check(ctx context.Context, call agentcore.ToolCall) error {
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

	case "glob", "grep", "ls":
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

		// Balanced + interactive: prompt user for obviously destructive commands.
		if isDangerousCommand(cmd) {
			return e.confirmDangerous(ctx, cmd)
		}
		return nil

	default:
		if e.profile == ProfileStrict {
			return fmt.Errorf("policy: tool %q denied in strict profile", call.Name)
		}
		return nil
	}
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

	// Resolve symlinks to prevent traversal via symbolic links.
	// For non-existent paths (write target), resolve the existing parent.
	p = resolveSymlinks(p)

	if !isSubPath(e.workspace, p) {
		return fmt.Errorf("policy: path outside workspace denied: %s", path)
	}
	return nil
}

func (e *Engine) confirmDangerous(ctx context.Context, cmd string) error {
	key := dangerousBase(cmd)
	if e.sessionAllowed(key) || e.persistedAllowed(key) {
		return nil
	}
	e.mu.RLock()
	fn := e.confirmFn
	e.mu.RUnlock()
	if fn == nil {
		return fmt.Errorf("policy: dangerous command denied")
	}
	decision, err := fn(ctx, PermitRequest{
		Tool:    "bash",
		Command: cmd,
		Reason:  "dangerous command: " + key,
	})
	if err != nil {
		return fmt.Errorf("policy: permission prompt: %w", err)
	}
	switch decision {
	case PermitAllowOnce:
		return nil
	case PermitAllowSession:
		e.addSessionAllow(key)
		return nil
	case PermitAllowAlways:
		e.addPersisted(key)
		return nil
	default:
		return fmt.Errorf("policy: user denied command")
	}
}

func (e *Engine) persistedAllowed(key string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.persisted[key]
	return ok
}

func (e *Engine) addPersisted(key string) {
	e.mu.Lock()
	if e.persisted == nil {
		e.persisted = make(map[string]struct{})
	}
	e.persisted[key] = struct{}{}
	fn := e.persistFn
	e.mu.Unlock()
	// Also add to session allow-list for immediate effect.
	e.addSessionAllow(key)
	if fn != nil {
		_ = fn(key) // best-effort persist; already allowed in memory
	}
}

// dangerousBase extracts the base command name that triggers the dangerous check.
func dangerousBase(cmd string) string {
	s := strings.TrimSpace(strings.ToLower(cmd))
	for _, seg := range splitShellSegments(s) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		fields := commandFields(seg)
		if len(fields) == 0 {
			continue
		}
		base := commandBase(fields[0])
		rest := fields[1:]
		base, _ = unwrapPrefix(base, rest)
		if base != "" {
			return base
		}
	}
	return "unknown"
}

// resolveSymlinks resolves symlinks in a path. For non-existent paths,
// it resolves the longest existing ancestor and appends the remaining tail.
func resolveSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	// Walk up to find the deepest existing ancestor.
	dir := filepath.Dir(p)
	tail := filepath.Base(p)
	for dir != p {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, tail)
		}
		tail = filepath.Join(filepath.Base(dir), tail)
		p = dir
		dir = filepath.Dir(dir)
	}
	return p
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
			if len([]rune(vv)) > 200 {
				vv = string([]rune(vv)[:200]) + "..."
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

	// Unwrap command wrappers so "env sudo rm -rf /" is treated as "sudo rm -rf /".
	base, rest = unwrapPrefix(base, rest)
	if base == "" {
		return false
	}

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

// unwrapPrefix strips command wrappers (env, command, builtin, eval, exec)
// to expose the real command. For example:
//
//	"env" ["sudo", "rm"] → "sudo" ["rm"]
//	"command" ["-v", "rm"] → "rm" []
//	"eval" ["rm", "-rf", "/"] → "rm" ["-rf", "/"]
func unwrapPrefix(base string, rest []string) (string, []string) {
	for len(rest) > 0 {
		switch base {
		case "env":
			// Skip flags (-i, -u, etc.) and inline assignments (FOO=bar).
			for len(rest) > 0 && (strings.HasPrefix(rest[0], "-") || isEnvAssignment(rest[0])) {
				rest = rest[1:]
			}
		case "command", "builtin", "exec":
			for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
				rest = rest[1:]
			}
		case "eval":
			// eval takes no flags; next token is the command.
		default:
			return base, rest
		}
		if len(rest) == 0 {
			return "", nil
		}
		base = commandBase(rest[0])
		rest = rest[1:]
	}
	return base, rest
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
		case "/", "/*", "/.", "/..", "~", "~/*",
			".", "./", "..", "../", "*", "../*":
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
