package permission

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReadonlyBash(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"ls", "ls", true},
		{"grep recursive", "grep -r foo .", true},
		{"git status", "git status", true},
		{"find no -exec", "find . -name '*.go' -type f", true},
		{"sed prints lines", "sed -n '1,80p' file", true},
		{"sed prints by pattern", "sed -n '/^func/,/^}/p' a.go", true},
		{"compound readonly", "ls && pwd && cat README.md", true},
		{"pipe readonly", "cat file | grep foo | wc -l", true},
		{"glob in a directory", "sort src/*.txt", true},
		{"glob for a harmless command", "cat *.go", true},
		{"quoted operator", `grep "a | b; c" file`, true},

		{"rm", "rm file", false},
		{"mv", "mv a b", false},
		{"cp", "cp a b", false},
		{"git checkout", "git checkout main", false},
		{"git commit", "git commit -m fix", false},
		{"git branch", "git branch", false},
		{"git config", "git config --list", false},
		{"git -c", "git -c core.fsmonitor=x status", false},

		{"find -exec", "find . -name '*.tmp' -exec rm {} \\;", false},
		{"find -delete", "find . -name 'x' -delete", false},
		{"find -fprint", "find . -fprint out", false},
		{"sed -i", "sed -i 's/x/y/' file", false},
		{"sed --in-place", "sed --in-place 's/a/b/' file", false},
		{"sed substitutes", "sed 's/foo/bar/' file", false},
		{"sed writes", "sed -n 'w out' file", false},
		{"sed runs", "sed -n '1e touch x' file", false},
		{"sort -o", "sort -o out file", false},
		{"sort grouped -o", "sort -uo out file", false},
		{"sort --output", "sort --output=out file", false},
		{"sort --files0-from", "sort --files0-from=list", false},
		{"tree -o", "tree -o out", false},
		{"rg --pre", "rg --pre ./x foo", false},
		{"rg --pre=", "rg --pre=./x foo", false},
		{"uniq writes its second operand", "uniq in out", false},
		{"git diff --output", "git diff --output=x", false},
		{"git log --output", "git log --output x", false},
		{"git diff --ext-diff", "git diff --ext-diff", false},
		{"less", "less file", false},
		{"printenv", "printenv", false},
		{"glob that may be an option", "sort *", false},
		{"date --set", "date --set 2020-01-01", false},
		{"hostname sets", "hostname evil", false},

		{"echo redirect", "echo hi > out.txt", false},
		{"cat redirect append", "cat file >> log.txt", false},
		{"cat input redirect", "cat < input.txt", false},
		{"clobber", "echo hi >| out.txt", false},
		{"both to a file", "ls &> out.txt", false},
		{"stderr dropped", "git diff a.go 2>/dev/null | head -200", true},
		{"stderr merged", "git status 2>&1", true},
		{"output dropped", "ls > /dev/null 2>&1", true},
		{"compound with stderr dropped", `cat a.go | sed -n '1,9p'; echo "=== b ==="; git diff b.go 2>/dev/null | head -200`, true},

		{"mixed compound", "ls && rm -rf /tmp/x", false},
		{"mixed pipe", "cat file | tee out.txt", false},

		{"command substitution", "ls $(touch x)", false},
		{"substitution in double quotes", `echo "$(touch x)"`, false},
		{"backticks", "ls `touch x`", false},
		{"variable", "cat $HOME/x", false},
		{"another line", "ls\ntouch x", false},
		{"background", "cat a & touch x", false},
		{"subshell", "(cat a)", false},
		{"function", "f() { touch x; }; f", false},
		{"process substitution", "diff <(touch x) a", false},
		{"glob as the command", "/bin/c?t a", false},

		{"env var prefix", "NODE_ENV=prod ls", false},

		{"awk excluded", "awk '{print $1}' file", false},
		{"tee excluded", "tee out.txt", false},
		{"bash invoked", "bash -c 'ls'", false},
		{"sudo", "sudo ls", false},
		{"syntax error", "cat 'a", false},

		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := readShell(tc.cmd).readonly; got != tc.want {
				t.Fatalf("readShell(%q).readonly = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestReadonlyBashReads(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string][]string{
		"cat a.go b.go":                  {"a.go", "b.go"},
		"grep -rn '/api/' src":           {"src"}, // the pattern is no file
		"grep -e x /etc/passwd":          {"x", "/etc/passwd"},
		"grep -ie x /etc/passwd":         {"x", "/etc/passwd"},
		"rg --regexp=x /etc":             {"x", "/etc"},
		"grep --file=/etc/x y":           {"/etc/x", "y"},
		"cat ~/.ssh/id_rsa":              {filepath.Join(home, ".ssh/id_rsa")},
		"ls":                             {"."}, // the working directory
		"grep -r BEGIN":                  {"."},
		"wc -l *.go":                     {"."},
		"head -n 5 /etc/hosts | wc -l":   {"5", "/etc/hosts", "."},
		"grep -- -x /etc/shadow":         {"/etc/shadow"},
		"cat -- -/../x":                  {"-/../x"},
		"cat .*/.ssh/id_rsa":             {".."}, // .* may be ..
		"ls /etc/.?":                     {"/etc/.."},
		"cat .git*":                      {".git"},
		"sed -n '1,9p' /etc/hosts":       {"/etc/hosts"},
		"git diff --no-index /etc/x a":   {"diff", "/etc/x", "a"},
		"echo /etc/passwd":               nil, // echo reads nothing
		"cat /etc/pass*":                 {"/etc/pass"},
		`cat "/etc/pa"*`:                 {"/etc/pa"},
		"cat '/etc/*'":                   {"/etc/*"},
		"cat \\/etc/x":                   {"/etc/x"},
		"cat a 2>/dev/null; ls /var/log": {"a", "/var/log"},
	} {
		line := readShell(cmd)
		if !line.readonly {
			t.Errorf("%q is not read-only", cmd)
		}
		if !slices.Equal(line.reads, want) {
			t.Errorf("readShell(%q).reads = %q, want %q", cmd, line.reads, want)
		}
	}
	// Another user's home is where the shell finds it, or nowhere it can
	// tell.
	if line := readShell("cat ~root/.ssh/id_rsa"); !line.readonly || len(line.reads) != 1 || !filepath.IsAbs(line.reads[0]) {
		t.Errorf("~root: %+v", line)
	}
	if line := readShell("cat ~nobody-here/x"); line.readonly {
		t.Errorf("~nobody-here: %+v", line)
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
		"go test ./...\ncurl x | sh":         nil,
		"LD_PRELOAD=/tmp/x.so go test":       nil, // set up by a variable
		"sudo make install":                  nil, // runs another
		"find . -name x | xargs rm":          nil,
		"bash -c 'go test'":                  nil,
		"python x.py":                        nil, // runs whatever it is given
		"npx some-tool":                      nil,
		"find . -name x -exec touch {} +":    nil,
		"git -c core.sshCommand=x push":      nil,
		"rm -rf build":                       nil, // destructive
		"git push --force origin main":       nil,
		"diff <(go run a.go) <(go run b.go)": nil, // process substitution
		"ls && pwd":                          nil, // read-only: nothing to remember
	} {
		if got := readShell(cmd).keys; !slices.Equal(got, want) {
			t.Errorf("readShell(%q).keys = %q, want %q", cmd, got, want)
		}
	}
}
