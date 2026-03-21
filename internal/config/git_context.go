package config

import (
	"fmt"
	"os/exec"
	"strings"
)

// CollectGitSnapshot runs git commands in cwd and returns a formatted
// snapshot suitable for LLM system prompt injection. Returns empty
// string when cwd is not a git repository.
func CollectGitSnapshot(cwd string) string {
	branch := gitExec(cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		return ""
	}

	mainBranch := detectMainBranch(cwd)
	status := gitExec(cwd, "status", "--short")
	log := gitExec(cwd, "log", "--oneline", "-n", "10")

	if status == "" {
		status = "(clean)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "This is the git status at the start of the conversation. "+
		"Note that this status is a snapshot in time, and will not update during the conversation.\n\n")
	fmt.Fprintf(&b, "Current branch: %s\n", branch)
	if mainBranch != "" {
		fmt.Fprintf(&b, "\nMain branch (you will usually use this for PRs): %s\n", mainBranch)
	}
	fmt.Fprintf(&b, "\nStatus:\n%s\n", status)
	if log != "" {
		fmt.Fprintf(&b, "\nRecent commits:\n%s\n", log)
	}
	return b.String()
}

// detectMainBranch tries to determine the main/default branch name.
func detectMainBranch(cwd string) string {
	// Try origin HEAD symbolic ref first.
	ref := gitExec(cwd, "symbolic-ref", "refs/remotes/origin/HEAD")
	if ref != "" {
		return strings.TrimPrefix(ref, "refs/remotes/origin/")
	}
	// Fallback: check if common branch names exist.
	for _, name := range []string{"main", "master"} {
		if gitExec(cwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+name) != "" {
			return name
		}
	}
	return ""
}

func gitExec(cwd string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
