package approval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/voocel/agentcore/permission"
)

func mkReq(tool, key, path string) permission.Request {
	args, _ := json.Marshal(map[string]string{key: path})
	return permission.Request{ToolName: tool, Args: args}
}

func TestCheckDangerousPath_ForceAsk(t *testing.T) {
	home := t.TempDir()

	tests := []struct {
		name string
		req  permission.Request
	}{
		// leak-class on read
		{"read ssh rsa key", mkReq("read", "file_path", filepath.Join(home, ".ssh", "id_rsa"))},
		{"read ssh ed25519 key", mkReq("read", "file_path", filepath.Join(home, ".ssh", "id_ed25519"))},
		{"read authorized_keys", mkReq("read", "file_path", filepath.Join(home, ".ssh", "authorized_keys"))},
		{"read aws credentials", mkReq("read", "file_path", filepath.Join(home, ".aws", "credentials"))},
		{"read aws config", mkReq("read", "file_path", filepath.Join(home, ".aws", "config"))},
		{"read netrc", mkReq("read", "file_path", filepath.Join(home, ".netrc"))},
		{"read pgpass", mkReq("read", "file_path", filepath.Join(home, ".pgpass"))},
		{"glob authorized_keys", mkReq("glob", "path", filepath.Join(home, ".ssh", "authorized_keys"))},
		{"read gcloud creds", mkReq("read", "file_path", filepath.Join(home, ".config", "gcloud", "credentials.db"))},

		// leak-class on write
		{"write authorized_keys", mkReq("write", "file_path", filepath.Join(home, ".ssh", "authorized_keys"))},
		{"write ssh private key", mkReq("write", "file_path", filepath.Join(home, ".ssh", "id_rsa"))},
		{"write aws credentials", mkReq("write", "file_path", filepath.Join(home, ".aws", "credentials"))},
		{"write netrc", mkReq("edit", "file_path", filepath.Join(home, ".netrc"))},

		// implant-class on write
		{"write bashrc", mkReq("write", "file_path", filepath.Join(home, ".bashrc"))},
		{"write zshrc", mkReq("write", "file_path", filepath.Join(home, ".zshrc"))},
		{"write profile", mkReq("write", "file_path", filepath.Join(home, ".profile"))},
		{"write envrc", mkReq("write", "file_path", filepath.Join(home, ".envrc"))},
		{"write gitconfig", mkReq("write", "file_path", filepath.Join(home, ".gitconfig"))},
		{"write mcp config", mkReq("edit", "file_path", filepath.Join(home, ".mcp.json"))},
		{"write claude config", mkReq("edit", "file_path", filepath.Join(home, ".claude.json"))},
		{"write into .git/hooks", mkReq("write", "file_path", filepath.Join(home, "proj", ".git", "hooks", "post-commit"))},
		{"write .git/config", mkReq("edit", "file_path", filepath.Join(home, "proj", ".git", "config"))},
		{"write .ssh/config", mkReq("write", "file_path", filepath.Join(home, ".ssh", "config"))},
		{"write .gnupg/something", mkReq("write", "file_path", filepath.Join(home, ".gnupg", "trustdb.gpg"))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if reason := CheckDangerousPath(home, tc.req); reason == "" {
				t.Fatalf("expected force-ask, got allow")
			}
		})
	}
}

func TestCheckDangerousPath_Allowed(t *testing.T) {
	home := t.TempDir()

	tests := []struct {
		name string
		req  permission.Request
	}{
		{"read ssh public key", mkReq("read", "file_path", filepath.Join(home, ".ssh", "id_rsa.pub"))},
		{"read .bashrc is not credential", mkReq("read", "file_path", filepath.Join(home, ".bashrc"))},
		{"read normal source", mkReq("read", "file_path", filepath.Join(home, "proj", "main.go"))},
		{"write normal source", mkReq("write", "file_path", filepath.Join(home, "proj", "main.go"))},
		{"write .git/info/exclude is harmless", mkReq("write", "file_path", filepath.Join(home, "proj", ".git", "info", "exclude"))},
		{"write .git/branches is harmless", mkReq("write", "file_path", filepath.Join(home, "proj", ".git", "branches", "x"))},
		{"bash has no path", mkReq("bash", "command", "ls -la")},
		{"empty args", permission.Request{ToolName: "write"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if reason := CheckDangerousPath(home, tc.req); reason != "" {
				t.Fatalf("expected allow, got reason=%q", reason)
			}
		})
	}
}

func TestCheckDangerousPath_CaseInsensitive(t *testing.T) {
	// macOS / Windows filesystems collapse case. A model writing .BASHRC must
	// not bypass the .bashrc force-ask rule.
	home := t.TempDir()
	tests := []string{".BASHRC", ".BaShRc", ".ZsHrC", ".GitConfig", ".MCP.JSON"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			req := mkReq("write", "file_path", filepath.Join(home, name))
			if reason := CheckDangerousPath(home, req); reason == "" {
				t.Fatalf("case-variant %q should still match force-ask", name)
			}
		})
	}
}

func TestCheckDangerousPath_RelativeResolvedAgainstWorkspace(t *testing.T) {
	ws := t.TempDir()
	req := mkReq("write", "file_path", ".bashrc")

	if reason := CheckDangerousPath(ws, req); reason == "" {
		t.Fatalf("relative .bashrc under workspace should match, got allow")
	}
}

func TestCheckDangerousPath_SymlinkDotfilesPattern(t *testing.T) {
	// Real-world dotfile setup: ~/.bashrc → ~/dotfiles/bashrc. The raw path
	// passed by the model is the .bashrc one, but resolving the symlink lands
	// on a basename without the dot. We must match the raw form.
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
	if reason := CheckDangerousPath(home, req); reason == "" {
		t.Fatalf("symlinked ~/.bashrc → ~/dotfiles/bashrc must still match force-ask")
	}
}

func TestCheckDangerousPath_BashCommandReferencesSSHKey(t *testing.T) {
	// Plug the gap where bash readonly-fastpath would otherwise leak credentials
	// embedded in the command string (no file_path arg to scan).
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
		{"ls listing only", "ls -la /tmp", false},
		{"no path at all", "pwd", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := mkReq("bash", "command", tc.cmd)
			reason := CheckDangerousPath(home, req)
			gotHit := reason != ""
			if gotHit != tc.wantHit {
				t.Fatalf("hit=%v want=%v reason=%q", gotHit, tc.wantHit, reason)
			}
		})
	}
}

func TestCheckDangerousPath_IDEAndAgentLoaderDirs(t *testing.T) {
	// .vscode/.idea/.claude are loader-execution surfaces (tasks autorun,
	// runConfigurations autolaunch, .claude hooks fire) — force-ask, never
	// hard-deny.
	home := t.TempDir()
	cases := []struct {
		name string
		path string
	}{
		{"vscode tasks.json", filepath.Join(home, "proj", ".vscode", "tasks.json")},
		{"vscode settings.json", filepath.Join(home, "proj", ".vscode", "settings.json")},
		{"idea runConfig", filepath.Join(home, "proj", ".idea", "runConfigurations", "x.xml")},
		{"claude hooks", filepath.Join(home, "proj", ".claude", "hooks.json")},
		// codebot self-config: only the hooks-bearing files, not memory/plans.
		{"codebot settings", filepath.Join(home, "proj", ".codebot", "settings.json")},
		{"codebot settings.local", filepath.Join(home, "proj", ".codebot", "settings.local.json")},
		{"codebot command def", filepath.Join(home, "proj", ".codebot", "commands", "deploy.md")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mkReq("write", "file_path", tc.path)
			if reason := CheckDangerousPath(home, req); reason == "" {
				t.Fatalf("expected force-ask, got allow")
			}
		})
	}
}

func TestCheckDangerousPath_CodebotMemoryAndPlansAreNotForceAsk(t *testing.T) {
	// memory/plans/sessions are harness-managed; they get force-ask only if
	// we screw up. Guard against accidentally over-matching .codebot/.
	home := t.TempDir()
	cases := []string{
		filepath.Join(home, ".codebot", "memory", "user.md"),
		filepath.Join(home, ".codebot", "plans", "x.md"),
		filepath.Join(home, ".codebot", "projects", "p1", "session.jsonl"),
	}
	for _, p := range cases {
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			req := mkReq("write", "file_path", p)
			if reason := CheckDangerousPath(home, req); reason != "" {
				t.Fatalf("harness-managed %q should not match dangerous-path (reason=%q)", p, reason)
			}
		})
	}
}

func TestCheckDangerousPath_SymlinkAttackPattern(t *testing.T) {
	// Inverse: attacker plants project/innocent → ~/.ssh/id_rsa. The raw path
	// is innocuous; only the resolved form reveals the leak.
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
	if reason := CheckDangerousPath(home, req); reason == "" {
		t.Fatalf("symlink to SSH private key must be caught via resolved form")
	}
}
