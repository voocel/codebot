// Package arch checks the dependency rules between codebot's packages
// (docs/refactor-plan.md §4.1).
package arch

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/voocel/codebot/"

// allowed lists, for a package or a package prefix ending in "/", the
// codebot packages it may import. Packages not listed are unconstrained.
var allowed = map[string][]string{
	// The frontends drive the core through app alone; config and todo are
	// plain data they also show.
	"internal/ui/": {"internal/app", "internal/session", "internal/interact", "internal/config", "internal/todo", "internal/ui/"},
	"internal/acp": {"internal/app", "internal/session", "internal/interact"},
	// The session knows the kernel and its log, nothing else.
	"internal/session":    {"internal/storage"},
	"internal/interact":   {"internal/permission"},
	"internal/todo":       {},
	"internal/storage":    {},
	"internal/permission": {},
	"internal/approval":   {"internal/config", "internal/interact", "internal/permission"},
	"internal/tools":      {"internal/interact", "internal/permission", "internal/skill", "internal/todo"},
	"internal/prompt":     {"internal/config"},
	"internal/subagent":   {"internal/config", "internal/prompt", "internal/provider", "internal/tools"},
}

// external lists the non-standard modules a package may import, where that
// is constrained.
var external = map[string][]string{
	"internal/session": {"github.com/voocel/agentcore", "github.com/voocel/litellm"},
}

func TestDependencyRules(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, module+"...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		pkg, imports := strings.TrimPrefix(fields[0], module), fields[1:]
		for _, imp := range imports {
			if err := check(pkg, imp); err != "" {
				t.Errorf("%s imports %s: %s", pkg, imp, err)
			}
		}
	}
}

// check returns why pkg may not import imp, or "".
func check(pkg, imp string) string {
	if dep, ok := strings.CutPrefix(imp, module); ok {
		if dep == "internal/app" || strings.HasPrefix(dep, "internal/ui/") || dep == "internal/acp" {
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

func ruleFor(pkg string) ([]string, bool) {
	for key, rule := range allowed {
		if matches(key, pkg) {
			return rule, true
		}
	}
	return nil, false
}

// matches reports whether pattern, a package or a prefix ending in "/",
// covers pkg.
func matches(pattern, pkg string) bool {
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(pkg, pattern)
	}
	return pkg == pattern
}

func isFrontend(pkg string) bool {
	return strings.HasPrefix(pkg, "internal/ui/") || pkg == "internal/acp"
}

func isStdlib(imp string) bool {
	return !strings.Contains(strings.Split(imp, "/")[0], ".")
}

func TestCheckCatchesViolations(t *testing.T) {
	bad := [][2]string{
		{"internal/ui/tui", module + "internal/approval"},
		{"internal/acp", module + "internal/config"},
		{"internal/session", module + "internal/config"},
		{"internal/session", "github.com/charmbracelet/bubbletea"},
		{"internal/tools", module + "internal/app"},
		{"internal/plugin", module + "internal/ui/tui"},
	}
	for _, b := range bad {
		if check(b[0], b[1]) == "" {
			t.Errorf("%s importing %s passed", b[0], b[1])
		}
	}
	good := [][2]string{
		{"internal/ui/commands", module + "internal/ui/tui"},
		{"internal/session", "github.com/voocel/agentcore/context"},
		{"cmd/codebot", module + "internal/app"},
		{"internal/app", module + "internal/tools"},
	}
	for _, g := range good {
		if why := check(g[0], g[1]); why != "" {
			t.Errorf("%s importing %s failed: %s", g[0], g[1], why)
		}
	}
}
