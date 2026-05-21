package approval

import (
	"strings"
	"testing"
)

func TestDestructiveCommandWarning(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		wantWarning bool
		wantContain string // substring expected when wantWarning=true; "" to skip
	}{
		// Git
		{"git reset hard", "git reset --hard HEAD~1", true, "discard"},
		{"git push force", "git push --force origin main", true, "overwrite"},
		{"git push force-with-lease", "git push --force-with-lease origin main", true, "overwrite"},
		{"git push -f short", "git push -f origin feature", true, "overwrite"},
		{"git push normal", "git push origin main", false, ""},
		{"git clean -f", "git clean -f", true, "untracked"},
		{"git clean -fd", "git clean -fd", true, "untracked"},
		{"git checkout dot", "git checkout .", true, "working tree"},
		{"git stash drop", "git stash drop", true, "stashed"},
		{"git branch -D", "git branch -D feature", true, "force-delete"},
		{"git commit no-verify", "git commit --no-verify -m x", true, "safety hooks"},
		{"git commit amend", "git commit --amend -m fix", true, "rewrite"},
		{"git status", "git status", false, ""},

		// rm
		{"rm -rf", "rm -rf /tmp/x", true, "recursively force-remove"},
		{"rm -fr", "rm -fr /tmp/x", true, "recursively force-remove"},
		{"rm -r", "rm -r /tmp/x", true, "recursively remove"},
		{"rm -f", "rm -f /tmp/x", true, "force-remove"},
		{"rm one file", "rm /tmp/x", false, ""},
		{"compound rm-rf at tail", "ls && rm -rf /tmp/x", true, "recursively force-remove"},
		{"compound rm-rf at head", "rm -rf /tmp/x && ls", true, "recursively force-remove"},

		// privilege escalation
		{"sudo cmd", "sudo apt update", true, "elevated"},
		{"compound sudo", "cd /tmp && sudo rm x", true, "elevated"},
		{"sudoers in path is not sudo", "cat /etc/sudoers", false, ""},

		// DB
		{"drop table", "psql -c 'DROP TABLE users'", true, "drop or truncate"},
		{"truncate table", "psql -c 'TRUNCATE TABLE logs'", true, "drop or truncate"},
		{"delete from", `psql -c "DELETE FROM users;"`, true, "delete all rows"},

		// Infra
		{"kubectl delete", "kubectl delete pod foo", true, "Kubernetes"},
		{"terraform destroy", "terraform destroy -auto-approve", true, "Terraform"},

		// Benign
		{"empty", "", false, ""},
		{"ls -la", "ls -la", false, ""},
		{"grep something", "grep -r foo .", false, ""},
		{"build", "go build ./...", false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DestructiveCommandWarning(tc.cmd)
			if (got != "") != tc.wantWarning {
				t.Fatalf("warning=%q wantWarning=%v", got, tc.wantWarning)
			}
			if tc.wantContain != "" && !strings.Contains(got, tc.wantContain) {
				t.Fatalf("warning=%q does not contain %q", got, tc.wantContain)
			}
		})
	}
}
