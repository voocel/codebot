package approval

import "testing"

func TestIsReadonlyBash(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		// --- simple readonly ---
		{"ls", "ls", true},
		{"ls -la", "ls -la /tmp", true},
		{"pwd", "pwd", true},
		{"echo", "echo hello", true},
		{"cat file", "cat README.md", true},
		{"grep recursive", "grep -r foo .", true},
		{"git status", "git status", true},
		{"git log", "git log --oneline -5", true},
		{"git diff", "git diff HEAD~1", true},
		{"git show", "git show HEAD", true},
		{"git blame", "git blame README.md", true},
		{"find no -exec", "find . -name '*.go' -type f", true},
		{"sed no -i", "sed 's/foo/bar/' file", true},
		{"compound readonly", "ls && pwd && cat README.md", true},
		{"pipe readonly", "cat file | grep foo | wc -l", true},

		// --- non-readonly: mutating commands ---
		{"rm", "rm file", false},
		{"mv", "mv a b", false},
		{"cp", "cp a b", false},
		{"git checkout", "git checkout main", false},
		{"git commit", "git commit -m fix", false},
		{"git reset", "git reset --hard", false},
		{"git push", "git push", false},
		// non-whitelisted git subcommands fall through (one ask + allow-session covers).
		{"git branch", "git branch", false},
		{"git config", "git config --list", false},
		{"git remote", "git remote -v", false},
		{"git ls-files", "git ls-files", false},

		// --- non-readonly: dangerous flags inside otherwise-readonly cmd ---
		{"find -exec", "find . -name '*.tmp' -exec rm {} \\;", false},
		{"find -delete", "find . -name 'x' -delete", false},
		{"sed -i", "sed -i 's/x/y/' file", false},
		{"sed --in-place", "sed --in-place 's/a/b/' file", false},

		// --- non-readonly: redirection ---
		{"echo redirect", "echo hi > out.txt", false},
		{"cat redirect append", "cat file >> log.txt", false},
		{"cat input redirect", "cat < input.txt", false},

		// --- non-readonly: any segment of compound poisons ---
		{"mixed compound", "ls && rm -rf /tmp/x", false},
		{"mixed pipe", "cat file | tee out.txt", false},

		// --- non-readonly: env-var prefix rejected outright ---
		{"env var prefix", "NODE_ENV=prod ls", false},

		// --- excluded commands (intentionally not on whitelist) ---
		{"awk excluded", "awk '{print $1}' file", false},
		{"tee excluded", "tee out.txt", false},
		{"bash invoked", "bash -c 'ls'", false},
		{"sudo", "sudo ls", false},

		// --- sensitive paths poison readonly fast-path ---
		{"cat ssh key", "cat ~/.ssh/id_rsa", false},
		{"grep into netrc", "grep secret ~/.netrc", false},
		{"head aws creds", "head /Users/x/.aws/credentials", false},
		{"public key fine", "cat ~/.ssh/id_rsa.pub", true},

		// --- edge ---
		{"empty", "", false},
		{"only spaces", "   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadonlyBash(tc.cmd); got != tc.want {
				t.Fatalf("isReadonlyBash(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestBashPrefix(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"ls", "ls"},
		{"ls -la", "ls"},
		{"git commit -m 'fix x'", "git commit"},
		{`git commit -m "different message"`, "git commit"},
		{"npm run build", "npm run"},
		{"NODE_ENV=prod npm run build", "npm run"},
		{"FOO=1 BAR=2 ls", "ls"},
		{"rm -rf /tmp/x", "rm"},
		{"ls && pwd", "ls"}, // first segment wins
		{"", ""},
		{"   ", ""},
		// second-token not a subcommand shape → single-token prefix
		{"./script.sh arg", "./script.sh"},
		{"cat /tmp/file", "cat"},
		{"git status", "git status"},
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := bashPrefix(tc.cmd); got != tc.want {
				t.Fatalf("bashPrefix(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestHasUnquotedRedirect(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"ls", false},
		{"ls -la /tmp", false},
		{"echo > file", true},
		{"echo >> file", true},
		{"cat < file", true},
		{`echo "a > b"`, false}, // quoted >
		{`echo 'a > b'`, false}, // quoted >
		{`echo "x" > file`, true},
		{"echo \\> file", false}, // escaped >
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := hasUnquotedRedirect(tc.cmd); got != tc.want {
				t.Fatalf("hasUnquotedRedirect(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
