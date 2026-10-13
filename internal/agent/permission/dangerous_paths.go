package permission

import (
	"path/filepath"
	"strings"
)

// checkDangerousPath returns why a request must be confirmed every time, or
// "". Credential paths qualify for reads and writes, so one allow never lets
// later turns re-read them silently. Persistence paths (shell rc, .git/hooks,
// loader configs) qualify for writes, where one Allow Always would keep an
// implant alive.
//
// Nothing is hard-denied: the user may well want help with ~/.ssh/config.
//
// Matching is case-insensitive, as macOS and Windows filesystems are, and
// checks both the raw and the symlink-resolved path: ~/.bashrc →
// ~/dotfiles/bashrc matches only raw, project/innocent → /etc/passwd only
// resolved.
func checkDangerousPath(workspace, tool string, c classification) string {
	// bash paths hide in the command string; without this, cat ~/.ssh/id_rsa
	// would pass as a read-only command.
	if tool == "bash" {
		if r := scanBashForSensitiveRead(workspace, c.command); r != "" {
			return r + " referenced in bash command"
		}
		return ""
	}
	if c.path == "" {
		return ""
	}
	candidates := dangerousPathCandidates(workspace, c.path)

	switch tool {
	case "read", "glob", "grep", "ls":
		for _, p := range candidates {
			if r := matchSensitiveRead(p); r != "" {
				return r + " (" + p + ") requires per-invocation approval"
			}
		}
	case "write", "edit":
		for _, p := range candidates {
			if r := matchSensitiveWrite(p); r != "" {
				return r + " (" + p + ") requires per-invocation approval"
			}
		}
	}
	return ""
}

// scanBashForSensitiveRead tokenizes simply, by whitespace. Here-docs and
// command substitution can evade it, but such commands are never read-only,
// so they still get the regular exec prompt.
func scanBashForSensitiveRead(workspace, cmd string) string {
	for _, tok := range bashPathTokens(cmd) {
		for _, p := range dangerousPathCandidates(workspace, tok) {
			if r := matchSensitiveRead(p); r != "" {
				return r + " at " + p
			}
		}
	}
	return ""
}

// bashPathTokens counts backslashes so Windows paths can't slip past; a
// stray shell escape only costs a prompt.
func bashPathTokens(cmd string) []string {
	var out []string
	for t := range strings.FieldsSeq(cmd) {
		t = strings.Trim(t, `'"`)
		if t == "" || strings.HasPrefix(t, "-") {
			continue
		}
		if strings.ContainsAny(t, `/~\`) || strings.HasPrefix(t, ".") {
			out = append(out, t)
		}
	}
	return out
}

// matchSensitiveRead matches credential files. Their contents would enter
// the transcript, where a later tool call could exfiltrate them.
func matchSensitiveRead(p string) string {
	base, parent := splitLower(p)

	if parent == ".ssh" {
		if strings.HasPrefix(base, "id_") && !strings.HasSuffix(base, ".pub") {
			return "SSH private key"
		}
		if base == "authorized_keys" {
			return "SSH authorized_keys"
		}
	}
	// ~/.aws also caches tokens in sso/cache and cli/cache.
	if hasPathSegment(p, ".aws") {
		return "AWS credentials"
	}
	// Only these gcloud files hold credentials: credentials.db,
	// access_tokens.db, application_default_credentials.json and
	// legacy_credentials/.
	if hasPathSegment(p, "gcloud") && (strings.Contains(base, "credentials") || strings.HasPrefix(base, "access_tokens") || hasPathSegment(p, "legacy_credentials")) {
		return "gcloud credentials"
	}
	if base == ".netrc" || base == ".pgpass" || base == ".git-credentials" {
		return "credentials"
	}
	if parent == ".codebot" && base == "mcp-oauth.json" {
		return "MCP OAuth tokens"
	}
	// The GitHub CLI keeps its token here when there is no keyring:
	// ~/.config/gh, or %AppData%\GitHub CLI on Windows.
	if base == "hosts.yml" && (parent == "gh" || parent == "github cli") {
		return "GitHub CLI token"
	}
	return ""
}

// Persistent are the paths whose contents run later, unasked: git's
// directory, codebot's configuration, the configs editors and agents run on
// open, and the dotfiles shells and tools load. A write tool's write to one
// is confirmed every time, and the sandbox keeps commands from writing those
// at the top of the directories they may write.
var Persistent = []string{
	".git", ".gitmodules", ".gitconfig",
	".codebot/settings.json", ".codebot/consent.json", ".codebot/skills", ".codebot/agents", ".codebot/plugins", ".codebot/approvals", ".agents/skills",
	".vscode", ".idea", ".claude", ".claude.json", ".mcp.json",
	".bashrc", ".bash_profile", ".zshrc", ".zprofile", ".profile", ".envrc", ".ripgreprc",
	".ssh", ".gnupg",
}

// matchSensitiveWrite adds the persistent paths, anywhere, to the credential
// ones. Overwriting a credential can lock the user out.
func matchSensitiveWrite(p string) string {
	if r := matchSensitiveRead(p); r != "" {
		return r
	}
	lower := strings.ToLower(filepath.ToSlash(p))
	for _, name := range Persistent {
		if suffix := "/" + name; strings.HasSuffix(lower, suffix) || strings.Contains(lower, suffix+"/") {
			return name
		}
	}
	return ""
}

func splitLower(p string) (base, parent string) {
	p = filepath.ToSlash(p)
	base = strings.ToLower(filepath.Base(p))
	parent = strings.ToLower(filepath.Base(filepath.Dir(p)))
	return
}

func hasPathSegment(p, name string) bool {
	lower := strings.ToLower(filepath.ToSlash(p))
	target := "/" + strings.ToLower(name) + "/"
	return strings.Contains(lower, target)
}

// dangerousPathCandidates adds the symlink-resolved path when it differs. A
// file not yet written resolves through its parent directories, so a write
// through a link into .codebot/skills counts as one there.
func dangerousPathCandidates(workspace, raw string) []string {
	p := raw
	if !filepath.IsAbs(p) && workspace != "" {
		p = filepath.Join(workspace, p)
	}
	p = filepath.Clean(p)
	out := []string{p}
	if resolved := resolveSymlinks(p); resolved != p {
		out = append(out, resolved)
	}
	return out
}
