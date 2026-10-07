package permission

import "testing"

func TestDestructiveCommandWarning(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		wantWarning bool
	}{
		{"git reset hard", "git reset --hard HEAD~1", true},
		{"git push force", "git push --force origin main", true},
		{"git push -f short", "git push -f origin feature", true},
		{"git push normal", "git push origin main", false},
		{"git clean -f", "git clean -f", true},
		{"git checkout dot", "git checkout .", true},
		{"git stash drop", "git stash drop", true},
		{"git branch -D", "git branch -D feature", true},
		{"git commit no-verify", "git commit --no-verify -m x", true},
		{"git commit amend", "git commit --amend -m fix", true},
		{"git status", "git status", false},

		{"rm -rf", "rm -rf /tmp/x", true},
		{"rm -fr", "rm -fr /tmp/x", true},
		{"rm -r", "rm -r /tmp/x", true},
		{"rm -f", "rm -f /tmp/x", true},
		{"rm one file", "rm /tmp/x", false},
		{"compound rm-rf at tail", "ls && rm -rf /tmp/x", true},

		{"sudo cmd", "sudo apt update", true},
		{"compound sudo", "cd /tmp && sudo rm x", true},
		{"sudoers in path is not sudo", "cat /etc/sudoers", false},

		{"drop table", "psql -c 'DROP TABLE users'", true},
		{"delete from", `psql -c "DELETE FROM users;"`, true},

		{"kubectl delete", "kubectl delete pod foo", true},
		{"terraform destroy", "terraform destroy -auto-approve", true},

		{"build", "go build ./...", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := destructiveCommandWarning(tc.cmd); (got != "") != tc.wantWarning {
				t.Fatalf("warning=%q wantWarning=%v", got, tc.wantWarning)
			}
		})
	}
}
