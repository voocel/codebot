package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func mkReq(tool, key, path string) Request {
	args, _ := json.Marshal(map[string]string{key: path})
	return Request{ToolName: tool, Args: args}
}

func TestCheckDangerousPath_ForceAsk(t *testing.T) {
	home := t.TempDir()

	tests := []struct {
		name string
		req  Request
	}{
		{"read ssh rsa key", mkReq("read", "file_path", filepath.Join(home, ".ssh", "id_rsa"))},
		{"read authorized_keys", mkReq("read", "file_path", filepath.Join(home, ".ssh", "authorized_keys"))},
		{"read aws credentials", mkReq("read", "file_path", filepath.Join(home, ".aws", "credentials"))},
		{"read aws config", mkReq("read", "file_path", filepath.Join(home, ".aws", "config"))},
		{"read netrc", mkReq("read", "file_path", filepath.Join(home, ".netrc"))},
		{"read pgpass", mkReq("read", "file_path", filepath.Join(home, ".pgpass"))},
		{"read git-credentials", mkReq("read", "file_path", filepath.Join(home, ".git-credentials"))},
		{"read gh token", mkReq("read", "file_path", filepath.Join(home, ".config", "gh", "hosts.yml"))},
		{"read mcp oauth tokens", mkReq("read", "file_path", filepath.Join(home, ".codebot", "mcp-oauth.json"))},
		{"glob authorized_keys", mkReq("glob", "path", filepath.Join(home, ".ssh", "authorized_keys"))},
		{"read aws sso token", mkReq("read", "file_path", filepath.Join(home, ".aws", "sso", "cache", "token.json"))},
		{"read aws cli cache", mkReq("read", "file_path", filepath.Join(home, ".aws", "cli", "cache", "role.json"))},
		{"read gcloud creds", mkReq("read", "file_path", filepath.Join(home, ".config", "gcloud", "credentials.db"))},
		{"read gcloud tokens", mkReq("read", "file_path", filepath.Join(home, ".config", "gcloud", "access_tokens.db"))},
		{"read gcloud adc", mkReq("read", "file_path", filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"))},
		{"read gcloud legacy", mkReq("read", "file_path", filepath.Join(home, ".config", "gcloud", "legacy_credentials", "me@example.com", "adc.json"))},

		{"write authorized_keys", mkReq("write", "file_path", filepath.Join(home, ".ssh", "authorized_keys"))},
		{"write netrc", mkReq("edit", "file_path", filepath.Join(home, ".netrc"))},

		{"write bashrc", mkReq("write", "file_path", filepath.Join(home, ".bashrc"))},
		{"write bash_profile", mkReq("write", "file_path", filepath.Join(home, ".bash_profile"))},
		{"write zprofile", mkReq("write", "file_path", filepath.Join(home, ".zprofile"))},
		{"write profile", mkReq("write", "file_path", filepath.Join(home, ".profile"))},
		{"write envrc", mkReq("write", "file_path", filepath.Join(home, ".envrc"))},
		{"write gitmodules", mkReq("edit", "file_path", filepath.Join(home, "proj", ".gitmodules"))},
		{"write ripgreprc", mkReq("write", "file_path", filepath.Join(home, ".ripgreprc"))},
		{"write claude config", mkReq("edit", "file_path", filepath.Join(home, ".claude.json"))},
		{"write into .git/hooks", mkReq("write", "file_path", filepath.Join(home, "proj", ".git", "hooks", "post-commit"))},
		{"write .git/config", mkReq("edit", "file_path", filepath.Join(home, "proj", ".git", "config"))},
		{"write .ssh/config", mkReq("write", "file_path", filepath.Join(home, ".ssh", "config"))},
		{"write .gnupg/something", mkReq("write", "file_path", filepath.Join(home, ".gnupg", "trustdb.gpg"))},
		{"write .aws/sso cache", mkReq("write", "file_path", filepath.Join(home, ".aws", "sso", "cache", "token.json"))},

		{"write .BASHRC", mkReq("write", "file_path", filepath.Join(home, ".BASHRC"))},
		{"write .ZsHrC", mkReq("write", "file_path", filepath.Join(home, ".ZsHrC"))},
		{"write .GitConfig", mkReq("write", "file_path", filepath.Join(home, ".GitConfig"))},
		{"write .MCP.JSON", mkReq("write", "file_path", filepath.Join(home, ".MCP.JSON"))},

		{"write relative .bashrc", mkReq("write", "file_path", ".bashrc")},

		{"vscode tasks.json", mkReq("write", "file_path", filepath.Join(home, "proj", ".vscode", "tasks.json"))},
		{"idea runConfig", mkReq("write", "file_path", filepath.Join(home, "proj", ".idea", "runConfigurations", "x.xml"))},
		{"claude hooks", mkReq("write", "file_path", filepath.Join(home, "proj", ".claude", "hooks.json"))},

		{"codebot settings", mkReq("write", "file_path", filepath.Join(home, "proj", ".codebot", "settings.json"))},
		{"codebot consents", mkReq("write", "file_path", filepath.Join(home, ".codebot", "consent.json"))},
		{"codebot skill", mkReq("write", "file_path", filepath.Join(home, ".codebot", "skills", "deploy", "SKILL.md"))},
		{"codebot plugin cache", mkReq("write", "file_path", filepath.Join(home, ".codebot", "plugins", "cache", "github.com", "a", "b", "c", "mcp.json"))},
		{"shared skill", mkReq("write", "file_path", filepath.Join(home, "proj", "sub", ".agents", "skills", "deploy", "SKILL.md"))},
		{"codebot agent", mkReq("write", "file_path", filepath.Join(home, "proj", ".codebot", "agents", "reviewer.md"))},
		{"codebot approvals", mkReq("write", "file_path", filepath.Join(home, ".codebot", "approvals", "p1.json"))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if reason := checkDangerousPath(home, tc.req); reason == "" {
				t.Fatalf("expected force-ask, got allow")
			}
		})
	}
}

// codebot's data (memory, sessions, worktrees) must not match as its
// configuration.
func TestCheckDangerousPath_Allowed(t *testing.T) {
	home := t.TempDir()

	tests := []struct {
		name string
		req  Request
	}{
		{"read ssh public key", mkReq("read", "file_path", filepath.Join(home, ".ssh", "id_rsa.pub"))},
		{"read .bashrc is not credential", mkReq("read", "file_path", filepath.Join(home, ".bashrc"))},
		{"read normal source", mkReq("read", "file_path", filepath.Join(home, "proj", "main.go"))},
		{"read gcloud configuration", mkReq("read", "file_path", filepath.Join(home, ".config", "gcloud", "configurations", "config_default"))},
		{"read a project named gcloud", mkReq("read", "file_path", filepath.Join(home, "src", "gcloud", "main.go"))},
		{"write normal source", mkReq("write", "file_path", filepath.Join(home, "proj", "main.go"))},
		{"write .git/info/exclude is harmless", mkReq("write", "file_path", filepath.Join(home, "proj", ".git", "info", "exclude"))},
		{"bash has no path", mkReq("bash", "command", "ls -la")},
		{"empty args", Request{ToolName: "write"}},
		{"codebot memory", mkReq("write", "file_path", filepath.Join(home, ".codebot", "memory", "user.md"))},
		{"codebot session", mkReq("write", "file_path", filepath.Join(home, ".codebot", "projects", "p1", "session.jsonl"))},
		{"codebot worktree", mkReq("write", "file_path", filepath.Join(home, "proj", ".codebot", "worktrees", "fix", "main.go"))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if reason := checkDangerousPath(home, tc.req); reason != "" {
				t.Fatalf("expected allow, got reason=%q", reason)
			}
		})
	}
}

func TestCheckDangerousPath_SymlinkDotfilesPattern(t *testing.T) {
	// ~/.bashrc → ~/dotfiles/bashrc: the resolved name has no dot, so the raw
	// path must match.
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	home := t.TempDir()
	dotdir := filepath.Join(home, "dotfiles")
	if err := os.MkdirAll(dotdir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dotdir, "bashrc")
	if err := os.WriteFile(target, []byte("# bash"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".bashrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	req := mkReq("write", "file_path", link)
	if reason := checkDangerousPath(home, req); reason == "" {
		t.Fatalf("symlinked ~/.bashrc → ~/dotfiles/bashrc must still match force-ask")
	}
}

func TestCheckDangerousPath_BashCommandReferencesSSHKey(t *testing.T) {
	// bash has no file_path argument; the paths are in the command string.
	home := t.TempDir()

	tests := []struct {
		name    string
		cmd     string
		wantHit bool
	}{
		{"cat ssh key absolute", "cat " + filepath.Join(home, ".ssh", "id_rsa"), true},
		{"cat ssh key home tilde", "cat ~/.ssh/id_rsa", true},
		{"grep aws creds", "grep secret " + filepath.Join(home, ".aws", "credentials"), true},
		{"netrc quoted", `cat "` + filepath.Join(home, ".netrc") + `"`, true},
		{"env prefix then read key", "HOME=/foo cat ~/.ssh/id_ed25519", true},
		{"public key is fine", "cat ~/.ssh/id_rsa.pub", false},
		{"normal source", "cat " + filepath.Join(home, "main.go"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := mkReq("bash", "command", tc.cmd)
			reason := checkDangerousPath(home, req)
			gotHit := reason != ""
			if gotHit != tc.wantHit {
				t.Fatalf("hit=%v want=%v reason=%q", gotHit, tc.wantHit, reason)
			}
		})
	}
}

// A new file written through a link into codebot's configuration counts as
// written there.
func TestCheckDangerousPath_ThroughALinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	proj := t.TempDir()
	skills := filepath.Join(proj, ".codebot", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(skills, filepath.Join(proj, "docs")); err != nil {
		t.Fatal(err)
	}
	req := mkReq("write", "file_path", filepath.Join(proj, "docs", "evil", "SKILL.md"))
	if reason := checkDangerousPath(proj, req); reason == "" {
		t.Fatal("a skill written through a linked directory went unasked")
	}
}

func TestCheckDangerousPath_SymlinkAttackPattern(t *testing.T) {
	// project/innocent → ~/.ssh/id_rsa: only the resolved path reveals the
	// key.
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	home := t.TempDir()
	sshdir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshdir, 0o755); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(sshdir, "id_rsa")
	if err := os.WriteFile(key, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	innocent := filepath.Join(home, "innocent")
	if err := os.Symlink(key, innocent); err != nil {
		t.Fatal(err)
	}

	req := mkReq("read", "file_path", innocent)
	if reason := checkDangerousPath(home, req); reason == "" {
		t.Fatalf("symlink to SSH private key must be caught via resolved form")
	}
}
