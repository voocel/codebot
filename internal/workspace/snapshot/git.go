package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type gitRunner struct {
	gitDir   string
	workTree string
}

// baseArgs pins settings that would otherwise vary with the user's config
// and platform. quotepath=false keeps non-ASCII paths verbatim in -z output.
func (g gitRunner) baseArgs() []string {
	return []string{
		"--git-dir=" + g.gitDir,
		"--work-tree=" + g.workTree,
		"-c", "core.autocrlf=false",
		"-c", "core.longpaths=true",
		"-c", "core.symlinks=true",
		"-c", "core.quotepath=false",
		// A global core.fsmonitor would start a watcher daemon over the
		// workspace.
		"-c", "core.fsmonitor=false",
		// Only backgroundGC collects, so `git add` never races a gc.
		"-c", "gc.auto=0",
	}
}

// run folds stderr into the error.
func (g gitRunner) run(args ...string) (string, error) {
	cmd := exec.Command("git", append(g.baseArgs(), args...)...)
	// Run from the root so git prints paths relative to the work tree.
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

func (g gitRunner) runZ(args ...string) ([]string, error) {
	out, err := g.run(args...)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// noPromptEnv keeps git from blocking on a credential prompt.
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
