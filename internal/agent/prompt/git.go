package prompt

import (
	"fmt"
	"os/exec"
	"strings"
)

// Git snapshots the repository containing cwd; the body is empty outside a
// repository.
func Git(cwd string) Part {
	p := Part{Key: "git", Title: "Git"}
	branch := gitExec(cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		return p
	}

	mainBranch := detectMainBranch(cwd)
	status := gitExec(cwd, "status", "--short")
	log := gitExec(cwd, "log", "--oneline", "-n", "10")

	if status == "" {
		status = "(clean)"
	}

	var b strings.Builder
	b.WriteString("This is the git status when this note was written. It is a snapshot: it does not update as the repository changes.\n\n")
	fmt.Fprintf(&b, "Current branch: %s\n", branch)
	if mainBranch != "" {
		fmt.Fprintf(&b, "\nMain branch (you will usually use this for PRs): %s\n", mainBranch)
	}
	fmt.Fprintf(&b, "\nStatus:\n%s\n", status)
	if log != "" {
		fmt.Fprintf(&b, "\nRecent commits:\n%s\n", log)
	}
	p.Body = strings.TrimSuffix(b.String(), "\n")
	return p
}

func detectMainBranch(cwd string) string {
	ref := gitExec(cwd, "symbolic-ref", "refs/remotes/origin/HEAD")
	if ref != "" {
		return strings.TrimPrefix(ref, "refs/remotes/origin/")
	}
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
