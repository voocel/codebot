package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ContextFiles holds the loaded context file contents.
type ContextFiles struct {
	// Agents is the concatenated content of all AGENTS.md files found
	// from filesystem root down to cwd, separated by newlines.
	Agents string

	// SystemOverride is the content of SYSTEM.md if found in cwd.
	// When non-empty, it replaces the default system prompt entirely.
	SystemOverride string

	// SystemAppend is the content of APPEND_SYSTEM.md if found in cwd.
	// When non-empty, it is appended to the system prompt.
	SystemAppend string
}

// LoadContextFiles searches for context files from cwd upward to the filesystem root.
//
// Loading order (lowest to highest specificity):
//  1. ~/.codebot/AGENTS.md (global user-level)
//  2. AGENTS.md in each ancestor from root down to cwd
//
// CLAUDE.md is used as fallback when AGENTS.md is not found in a directory.
// SYSTEM.md and APPEND_SYSTEM.md are only looked for in cwd.
func LoadContextFiles(cwd string) ContextFiles {
	var cf ContextFiles
	var agentParts []string

	// Global user-level AGENTS.md (lowest priority).
	if dir := UserConfigDir(); dir != "" {
		if content := readAgentFile(dir); content != "" {
			agentParts = append(agentParts, content)
		}
	}

	// Walk from root to cwd, collecting AGENTS.md (fallback: CLAUDE.md).
	for _, dir := range parentChain(cwd) {
		if content := readAgentFile(dir); content != "" {
			agentParts = append(agentParts, content)
		}
	}

	cf.Agents = strings.Join(agentParts, "\n\n---\n\n")

	// SYSTEM.md and APPEND_SYSTEM.md only in cwd
	cf.SystemOverride = readFileOr(filepath.Join(cwd, "SYSTEM.md"))
	cf.SystemAppend = readFileOr(filepath.Join(cwd, "APPEND_SYSTEM.md"))

	return cf
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
