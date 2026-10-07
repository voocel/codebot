package permission

import (
	"path"
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
func checkDangerousPath(workspace string, req Request) string {
	// bash paths hide in the command string; without this, cat ~/.ssh/id_rsa
	// would pass as a read-only command.
	if req.ToolName == "bash" {
		cmd := stringField(req.Args, "command")
		if r := scanBashForSensitiveRead(workspace, cmd); r != "" {
			return r + " referenced in bash command"
		}
		return ""
	}

	raw := pathField(req.Args)
	if raw == "" {
		return ""
	}
	candidates := dangerousPathCandidates(workspace, raw)

	switch req.ToolName {
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

// matchSensitiveWrite adds persistence paths to the credential ones.
// Overwriting a credential can lock the user out.
func matchSensitiveWrite(p string) string {
	if r := matchSensitiveRead(p); r != "" {
		return r
	}

	base, parent := splitLower(p)
	if reason, ok := forceAskBasenames[base]; ok {
		return reason
	}

	// codebot's own configuration decides what runs unasked: settings,
	// consent.json, skills, plugins, sub-agents and stored approvals.
	// Sessions, memory, snapshots and worktrees are data, not configuration.
	lower := strings.ToLower(filepath.ToSlash(p))
	if parent == ".codebot" && (base == "settings.json" || base == "consent.json") {
		return "codebot settings"
	}
	for _, dir := range []string{".codebot/skills", ".codebot/agents", ".codebot/plugins", ".codebot/approvals", ".agents/skills"} {
		if strings.Contains(lower, "/"+dir+"/") {
			return "codebot " + path.Base(dir)
		}
	}

	// .ssh and .gnupg hold identity. Editing .vscode, .idea or .claude once
	// runs code on every later open: tasks.json, runConfigurations and agent
	// hooks autorun.
	for _, seg := range []string{".ssh", ".gnupg"} {
		if parent == seg || hasPathSegment(p, seg) {
			return seg + " config"
		}
	}
	for _, seg := range []string{".vscode", ".idea"} {
		if hasPathSegment(p, seg) {
			return seg + " loader config"
		}
	}
	if hasPathSegment(p, ".claude") {
		return "Claude config"
	}

	// Hooks run on every commit and config sets remotes and identity. The
	// rest of .git is harmless or managed by git.
	if strings.Contains(lower, "/.git/hooks/") {
		return ".git hooks"
	}
	if strings.HasSuffix(lower, "/.git/config") {
		return ".git/config"
	}
	return ""
}

// forceAskBasenames covers the common persistence dotfiles only; rare ones
// such as .zshenv and .kshrc are left out.
var forceAskBasenames = map[string]string{
	".bashrc":       "shell rc",
	".bash_profile": "shell rc",
	".zshrc":        "shell rc",
	".zprofile":     "shell rc",
	".profile":      "shell rc",
	".envrc":        "direnv config",
	".gitconfig":    "git config",
	".gitmodules":   "git config",
	".ripgreprc":    "tool config",
	".mcp.json":     "MCP config",
	".claude.json":  "Claude config",
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
