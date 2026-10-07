package prompt

import (
	"path/filepath"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/regular"
)

// loadAgents joins the AGENTS.md files from least to most specific, so the
// closest one comes last.
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

// parentChain("/a/b") returns ["/", "/a", "/a/b"].
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

// readIn limits where symlinks in dir may lead. Files in ~/.codebot may point
// anywhere; project files must stay inside the project root, and other
// directories' files inside that directory. A repository must not be able to
// pull the user's other files into the prompt.
func readIn(root, dir string) func(string) ([]byte, error) {
	within := dir
	switch {
	case dir == config.UserConfigDir():
		return regular.ReadFile
	case root != "" && (dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))):
		within = root
	}
	return func(path string) ([]byte, error) { return regular.ReadFileIn(within, path) }
}

func readText(read func(string) ([]byte, error), path string) string {
	data, err := read(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
