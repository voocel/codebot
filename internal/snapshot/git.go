package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// gitRunner executes git against a shadow repository: a standalone --git-dir
// whose --work-tree points at the real workspace. Every snapshot operation
// stays isolated from the user's own .git (index, branches, reflog, history).
type gitRunner struct {
	gitDir   string
	workTree string
}

// baseArgs are prepended to every invocation. The core.* flags mirror
// opencode's shadow-repo config for cross-platform stability; quotepath=false
// keeps non-ASCII paths verbatim so -z output stays byte-clean for splitting.
func (g gitRunner) baseArgs() []string {
	return []string{
		"--git-dir=" + g.gitDir,
		"--work-tree=" + g.workTree,
		"-c", "core.autocrlf=false",
		"-c", "core.longpaths=true",
		"-c", "core.symlinks=true",
		"-c", "core.quotepath=false",
		// Pin fsmonitor off so a user's global core.fsmonitor can't make this
		// shadow repo spawn a filesystem-watcher daemon over their workspace.
		"-c", "core.fsmonitor=false",
	}
}

// run executes git and returns raw stdout. stderr is folded into the error.
func (g gitRunner) run(args ...string) (string, error) {
	cmd := exec.Command("git", append(g.baseArgs(), args...)...)
	// Anchor relative pathspecs and path output to the work-tree root. Without
	// this, running from a subdirectory of the work-tree makes git emit paths
	// relative to that subdir, breaking filepath.Join(workTree, rel).
	cmd.Dir = g.workTree
	cmd.Env = noPromptEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", firstArg(args), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runZ runs git and splits NUL-delimited stdout (the -z form) into entries.
func (g gitRunner) runZ(args ...string) ([]string, error) {
	out, err := g.run(args...)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// noPromptEnv disables git/SSH credential prompts so an operation against a
// private remote can never block the host process waiting on /dev/tty.
func noPromptEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
}

func splitNUL(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\x00")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return "?"
	}
	return args[0]
}
