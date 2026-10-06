// Package arch checks the dependency rules between codebot's packages. The
// directories under internal are its layers, each depending only on those
// below it:
//
//	ui/         frontends: tui, print, acp
//	app/        assembly: App and Conversation
//	extension/  what users plug in: skills, sub-agents, MCP servers, hooks
//	agent/      what the agent runs with: tools, skills, sub-agents, prompt,
//	            permissions, todos
//	session/    the conversation actor and its log
//	workspace/  what codebot does to the repository: checkpoints, worktrees
//	infra/      settings, models, telemetry
//
// agent, session and workspace share a layer and do not depend on one
// another; app joins them. interact, the contract with the frontends, and
// lib, generic helpers, are leaves any layer may use.
package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/voocel/codebot/"

// allowed lists, for a package or a package and those under it ("x/..."),
// the codebot packages it may import; the most specific pattern applies.
// Packages not listed are unconstrained.
var allowed = map[string][]string{
	// The frontends drive the core through app alone, and none depends on
	// another; settings and todos are plain data they also show.
	"internal/ui/...":        {"internal/app", "internal/session", "internal/interact", "internal/infra/config", "internal/agent/todo"},
	"internal/ui/tui/...":    {"internal/app", "internal/session", "internal/interact", "internal/infra/config", "internal/agent/todo", "internal/ui/tui/..."},
	"internal/extension/...": {"internal/extension/...", "internal/agent/...", "internal/workspace/...", "internal/infra/...", "internal/interact", "internal/lib/..."},
	"internal/agent/...":     {"internal/agent/...", "internal/infra/...", "internal/interact", "internal/lib/..."},
	"internal/workspace/...": {"internal/workspace/...", "internal/infra/...", "internal/lib/..."},
	// The session knows the kernel and its log, nothing else.
	"internal/session/...": {"internal/session/..."},
	"internal/infra/...":   {"internal/infra/...", "internal/lib/..."},
	// The contract with the frontends depends on nothing; what implements
	// or calls it does.
	"internal/interact": {},
	// Shared helpers know nothing of codebot, so any package may use them.
	"internal/lib/...": {"internal/lib/..."},
}

// external lists the non-standard modules a package may import, where that
// is constrained.
var external = map[string][]string{
	"internal/session": {"github.com/voocel/agentcore", "github.com/voocel/litellm"},
	// What the TUI shows of a conversation renders to plain strings, apart
	// from the event loop: it is the same live, restored and left on exit.
	"internal/ui/tui/transcript": {"github.com/voocel/agentcore", "github.com/voocel/litellm", "charm.land/lipgloss/v2", "github.com/charmbracelet/x/ansi"},
	"internal/ui/tui/markdown":   {"charm.land/lipgloss/v2", "github.com/charmbracelet/x/ansi", "github.com/yuin/goldmark"},
	"internal/ui/tui/syntax":     {"github.com/alecthomas/chroma/v2"},
	"internal/ui/tui/theme":      {"charm.land/lipgloss/v2"},
}

// TestDependencyRules reads the imports from the sources rather than from go
// list: the test cache sees the files a test opens, not those a command it
// runs does, so a cached pass would hide a new violation.
func TestDependencyRules(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	reported := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(dir)
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if why := check(pkg, imp); why != "" && !reported[pkg+" "+imp] {
				reported[pkg+" "+imp] = true
				t.Errorf("%s imports %s: %s", pkg, imp, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// check returns why pkg may not import imp, or "".
func check(pkg, imp string) string {
	if dep, ok := strings.CutPrefix(imp, module); ok {
		if dep == "internal/app" || isFrontend(dep) {
			// The core never depends on what is built on it.
			if !strings.HasPrefix(pkg, "cmd/") && !isFrontend(pkg) {
				return "only cmd and the frontends may use app and the frontends"
			}
		}
		if rule, ok := ruleFor(pkg); ok && !slices.ContainsFunc(rule, func(a string) bool { return matches(a, dep) }) {
			return "not in its allowed imports"
		}
		return ""
	}
	if mods, ok := external[pkg]; ok && !isStdlib(imp) && !slices.ContainsFunc(mods, func(m string) bool { return imp == m || strings.HasPrefix(imp, m+"/") }) {
		return "not in its allowed modules"
	}
	return ""
}

// ruleFor returns the rule of the most specific pattern covering pkg.
func ruleFor(pkg string) ([]string, bool) {
	best := ""
	for pattern := range allowed {
		if matches(pattern, pkg) && len(pattern) > len(best) {
			best = pattern
		}
	}
	rule, ok := allowed[best]
	return rule, ok
}

// matches reports whether pattern, a package or a package and those under it
// ("x/..."), covers pkg.
func matches(pattern, pkg string) bool {
	if dir, ok := strings.CutSuffix(pattern, "/..."); ok {
		return pkg == dir || strings.HasPrefix(pkg, dir+"/")
	}
	return pkg == pattern
}

func isFrontend(pkg string) bool { return matches("internal/ui/...", pkg) }

func isStdlib(imp string) bool {
	return !strings.Contains(strings.Split(imp, "/")[0], ".")
}

func TestCheckCatchesViolations(t *testing.T) {
	bad := [][2]string{
		{"internal/ui/tui", module + "internal/agent/permission"},
		{"internal/ui/acp", module + "internal/ui/tui"},
		{"internal/ui/print", module + "internal/ui/tui/markdown"},
		{"internal/agent/tools", module + "internal/app"},
		{"internal/agent/tools", module + "internal/extension/mcp"},
		{"internal/agent/permission", module + "internal/session"},
		{"internal/agent/tools", module + "internal/workspace/worktree"},
		{"internal/workspace/snapshot", module + "internal/agent/permission"},
		{"internal/infra/config", module + "internal/agent/prompt"},
		{"internal/extension", module + "internal/ui/tui"},
		{"internal/session", module + "internal/infra/config"},
		{"internal/session", "github.com/charmbracelet/bubbletea"},
		{"internal/ui/tui/transcript", "charm.land/bubbletea/v2"},
		{"internal/ui/tui/markdown", "charm.land/bubbles/v2/viewport"},
		{"internal/interact", module + "internal/agent/permission"},
		{"internal/lib/frontmatter", module + "internal/infra/config"},
	}
	for _, b := range bad {
		if check(b[0], b[1]) == "" {
			t.Errorf("%s importing %s passed", b[0], b[1])
		}
	}
	good := [][2]string{
		{"internal/ui/tui", module + "internal/ui/tui/commands"},
		{"internal/ui/tui/transcript", "github.com/voocel/agentcore/subagent"},
		{"internal/ui/tui", module + "internal/ui/tui/markdown"},
		{"internal/ui/acp", module + "internal/app"},
		{"internal/extension/mcp", module + "internal/agent/permission"},
		{"internal/agent/subagent", module + "internal/lib/frontmatter"},
		{"internal/agent/permission", module + "internal/infra/config"},
		{"internal/workspace/worktree", module + "internal/infra/config"},
		{"internal/session", module + "internal/session/storage"},
		{"internal/session", "github.com/voocel/agentcore/context"},
		{"cmd/codebot", module + "internal/app"},
		{"internal/app", module + "internal/agent/tools"},
	}
	for _, g := range good {
		if why := check(g[0], g[1]); why != "" {
			t.Errorf("%s importing %s failed: %s", g[0], g[1], why)
		}
	}
}
