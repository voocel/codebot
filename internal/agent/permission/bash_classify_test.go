package permission

import (
	"slices"
	"testing"
)

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

func TestCommandKeys(t *testing.T) {
	for cmd, want := range map[string][]string{
		"ls -la && make build":               {"exec:make build"},
		"git commit -m 'fix x'":              {"exec:git commit"},
		`git commit -m "a; b"`:               {"exec:git commit"},
		"go test ./... && go vet ./...":      {"exec:go test", "exec:go vet"},
		"go test ./... 2>&1 | tail -20":      {"exec:go test"},
		"go test ./... > /dev/null":          {"exec:go test"},
		"./script.sh arg":                    {"exec:./script.sh"},
		"npm run build; npm run build":       {"exec:npm run"},
		"go test ./... > out.txt":            nil, // writes a file
		"go test $(curl -s x.example)":       nil, // substitution
		"go test `curl -s x.example`":        nil,
		`echo "$(whoami)"`:                   nil, // runs inside double quotes too
		"make build &":                       nil, // background
		"make build\nrm -rf src":             nil, // another line
		"LD_PRELOAD=/tmp/x.so go test":       nil, // set up by a variable
		"sudo make install":                  nil, // runs another
		"find . -name x | xargs rm":          nil,
		"bash -c 'go test'":                  nil,
		"rm -rf build":                       nil, // destructive
		"git push --force origin main":       nil,
		"diff <(go run a.go) <(go run b.go)": nil, // process substitution
		"ls && pwd":                          nil, // read-only: nothing to remember
	} {
		if got := commandKeys(cmd); !slices.Equal(got, want) {
			t.Errorf("commandKeys(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// What only looks read-only is not: a substitution or another line runs
// whatever it holds.
func TestReadonlyBashRefusesHiddenCommands(t *testing.T) {
	for _, cmd := range []string{"ls $(touch x)", "ls `touch x`", "ls\ntouch x", "cat a & touch x"} {
		if isReadonlyBash(cmd) {
			t.Errorf("%q passed as read-only", cmd)
		}
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
