package prompt

import (
	"path/filepath"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/regular"
)

// loadAgents returns the AGENTS.md files that apply in cwd, joined from the
// least specific: ~/.codebot/AGENTS.md, then each directory from the
// filesystem root down to cwd. CLAUDE.md stands in for a directory without
// an AGENTS.md.
func loadAgents(cwd string) string {
	root := config.ProjectRoot(cwd)
	var parts []string
	for _, dir := range append([]string{config.UserConfigDir()}, parentChain(cwd)...) {
		read := readIn(root, dir)
		content := readText(read, filepath.Join(dir, "AGENTS.md"))
		if content == "" {
			content = readText(read, filepath.Join(dir, "CLAUDE.md"))
		}
		if content != "" {
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

// readIn returns how to read the files of dir. Those of the project at root
// may not lead outside it: the user's files are not the project's to read
// into the prompt.
func readIn(root, dir string) func(string) ([]byte, error) {
	if root != "" && (dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))) {
		return func(path string) ([]byte, error) { return regular.ReadFileIn(root, path) }
	}
	return regular.ReadFile
}

// readText returns the file at path, read with read and trimmed; "" for
// none.
func readText(read func(string) ([]byte, error), path string) string {
	data, err := read(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
