package permission

import (
	"path"
	"path/filepath"
	"strings"
)

// checkDangerousPath classifies a permission request's target path:
//
//	reason != ""  → confirm every call (classification.confirm): the mode
//	                and stored approvals are bypassed and only Allow Once /
//	                Deny are offered; deny rules still apply. Two flavours
//	                of path qualify:
//
//	                  leak-class (read or write): SSH keys, ~/.aws, gcloud
//	                  credentials, .netrc, .pgpass, .git-credentials, the
//	                  GitHub CLI's hosts.yml — auto-allowing once would
//	                  let later turns silently re-read them.
//
//	                  implant-class (write only): shell rc, .git/hooks,
//	                  .gitconfig, .mcp.json, .claude.json, .ssh / .gnupg
//	                  dirs, IDE & agent loader configs. A single Allow
//	                  Always would propagate the implant forever.
//
//	reason == ""  → clean. The request falls through to the regular
//	                permission pipeline.
//
// We deliberately do NOT hard-deny anything: the model is cooperative, the
// user can see the prompt, and a one-time Allow Once is a fine answer to
// "yes, look at my ~/.ssh/config to debug auth". The cost of asking is one
// click; the cost of a wrong hard-deny is the user can't get help at all.
//
// All comparisons are case-insensitive (macOS / Windows filesystems collapse
// case) and check both the raw user-supplied path AND the symlink-resolved
// path. Both forms are needed because dotfiles can be symlinked in either
// direction:
//
//	~/.bashrc → ~/dotfiles/bashrc  (chezmoi / stow):
//	  raw matches forceAskBasenames[".bashrc"];
//	  resolved would miss (.bashrc basename gone).
//
//	project/innocent → /etc/passwd  (attacker):
//	  raw would miss;
//	  resolved catches the real target.
func checkDangerousPath(workspace string, req Request) string {
	// bash needs special handling: paths are embedded in the command string,
	// not exposed as a structured argument. Without this, `bash cat ~/.ssh/id_rsa`
	// would bypass the read-side checks entirely (cat is on the readonly
	// whitelist, the bash tool itself has no `file_path` field).
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

// scanBashForSensitiveRead walks a bash command looking for path-like tokens
// that match the sensitive-read list (SSH keys, cloud credentials, .netrc
// and the like). Returns the matched-reason on first hit, or "" if clean. Used
// both to poison the readonly bash fast-path and to force-ask once the
// request reaches the engine.
//
// Tokenisation is intentionally simple (whitespace split, strip quotes) —
// good enough to catch:
//
//	cat ~/.ssh/id_rsa
//	grep secret /Users/x/.aws/credentials
//	HOME=/foo cat ~/.netrc
//	cat "~/.pgpass"
//
// Misses things a determined attacker could construct (here-docs, command
// substitution rewriting paths). For those the regular Exec ask flow still
// catches them in balanced mode.
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

// bashPathTokens returns tokens from cmd that look like they could be paths:
// contain a slash in either direction, start with ~, or start with .
// (relative path). Backslashes count so Windows absolute paths
// (C:\Users\...\.ssh\id_rsa) can't slip past; a stray bash escape that gets
// through only costs a force-ask, never a wrong allow. Skips flag-shaped
// tokens and strips surrounding quotes.
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

// matchSensitiveRead: paths whose contents are credential material. Reading
// these leaks secrets into the LLM transcript, from which a later tool call
// (web_fetch, bash curl) could exfiltrate. Force-ask catches both directions:
// the user sees the prompt and can decide.
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
	// Besides credentials and config, ~/.aws caches tokens: sso/cache for
	// SSO sign-ins, cli/cache for assumed roles.
	if hasPathSegment(p, ".aws") {
		return "AWS credentials"
	}
	// gcloud keeps credentials.db, access_tokens.db,
	// application_default_credentials.json and legacy_credentials/; the rest
	// of its directory (configurations, logs) holds none.
	if hasPathSegment(p, "gcloud") && (strings.Contains(base, "credentials") || strings.HasPrefix(base, "access_tokens") || hasPathSegment(p, "legacy_credentials")) {
		return "gcloud credentials"
	}
	if base == ".netrc" || base == ".pgpass" || base == ".git-credentials" {
		return "credentials"
	}
	// The GitHub CLI's token, where no keyring holds it: ~/.config/gh, or
	// on Windows %AppData%\GitHub CLI.
	if base == "hosts.yml" && (parent == "gh" || parent == "github cli") {
		return "GitHub CLI token"
	}
	return ""
}

// matchSensitiveWrite: write requests that earn a forced ask. Two cohorts:
//
//   - credentials (leak-class on write too — overwrite = lockout): what
//     matchSensitiveRead lists.
//   - persistence (implant-class): shell rc, .git/hooks, .gitconfig,
//     .mcp.json, .claude.json, IDE & agent loader configs. A single Allow
//     Always would propagate the implant forever, so we require per-call
//     consent regardless.
func matchSensitiveWrite(p string) string {
	if r := matchSensitiveRead(p); r != "" {
		return r
	}

	base, parent := splitLower(p)
	if reason, ok := forceAskBasenames[base]; ok {
		return reason
	}

	// codebot's own configuration decides what runs unasked: settings carry
	// hooks, MCP servers, plugins and permission rules; consent.json what the
	// user agreed to run; skills and plugins may run commands and allow
	// tools, then sub-agent definitions and stored approvals. The
	// harness-managed data beside them (sessions, memory, snapshots,
	// worktrees) is not configuration.
	lower := strings.ToLower(filepath.ToSlash(p))
	if parent == ".codebot" && (base == "settings.json" || base == "consent.json") {
		return "codebot settings"
	}
	for _, dir := range []string{".codebot/skills", ".codebot/agents", ".codebot/plugins", ".codebot/approvals", ".agents/skills"} {
		if strings.Contains(lower, "/"+dir+"/") {
			return "codebot " + path.Base(dir)
		}
	}

	// Whole-subtree force-ask: identity / credentials parent dirs +
	// IDE & agent loader configs. .vscode/tasks.json autorun on file open,
	// .idea/runConfigurations/*.xml are JetBrains autorun, .claude/ hosts
	// agent hooks. All "edit me once → execute on every future open"
	// patterns. Uses hasPathSegment so deeper paths like
	// .idea/runConfigurations/x.xml still match.
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

	// .git internals whose modification has lasting effect on the repo
	// (hooks fire on every commit; config changes remote/identity). Other
	// .git subdirs (info/, branches/, objects/) are deliberately left alone
	// — they're either harmless or pack-managed.
	if strings.Contains(lower, "/.git/hooks/") {
		return ".git hooks"
	}
	if strings.HasSuffix(lower, "/.git/config") {
		return ".git/config"
	}
	return ""
}

// forceAskBasenames lists the persistence-class dotfiles. Kept to the ones
// people actually use — .bash_login / .zshenv / .kshrc and friends are valid
// but vanishingly rare; if they ever matter we can add them back.
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

// splitLower returns the lower-cased basename and immediate parent dir name.
// Path is normalised to forward slashes first so we behave the same on
// Windows.
func splitLower(p string) (base, parent string) {
	p = filepath.ToSlash(p)
	base = strings.ToLower(filepath.Base(p))
	parent = strings.ToLower(filepath.Base(filepath.Dir(p)))
	return
}

// hasPathSegment reports whether name appears as a complete path segment
// in p (case-insensitive). Used for matches like "~/.config/gcloud/..."
// where the dir of interest is several levels above the basename.
func hasPathSegment(p, name string) bool {
	lower := strings.ToLower(filepath.ToSlash(p))
	target := "/" + strings.ToLower(name) + "/"
	return strings.Contains(lower, target)
}

// dangerousPathCandidates returns the path forms to match against: the
// workspace-resolved cleaned path, and where symlinks lead it elsewhere,
// the resolved form too. A file not yet written resolves through the
// directories above it: one written through a link to .codebot/skills is
// written there.
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
