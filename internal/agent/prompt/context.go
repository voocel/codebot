package prompt

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
)

// loadAgents returns the AGENTS.md files that apply in cwd, joined from the
// least specific: ~/.codebot/AGENTS.md, then each directory from the
// filesystem root down to cwd. CLAUDE.md stands in for a directory without
// an AGENTS.md.
func loadAgents(cwd string) string {
	var parts []string
	for _, dir := range append([]string{config.UserConfigDir()}, parentChain(cwd)...) {
		if content := readAgentFile(dir); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// parentChain returns directories from the root down to dir (inclusive).
// e.g. "/a/b/c" → ["/", "/a", "/a/b", "/a/b/c"]
func parentChain(dir string) []string {
	dir = filepath.Clean(dir)
	var chain []string
	for {
		chain = append([]string{dir}, chain...)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return chain
}

// readAgentFile returns the content of AGENTS.md (or CLAUDE.md fallback) in dir.
func readAgentFile(dir string) string {
	if content := readFileOr(filepath.Join(dir, "AGENTS.md")); content != "" {
		return content
	}
	return readFileOr(filepath.Join(dir, "CLAUDE.md"))
}

func readFileOr(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
