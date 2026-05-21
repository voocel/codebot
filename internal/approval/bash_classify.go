package approval

import (
	"regexp"
	"strings"
)

// isReadonlyBash reports whether a shell command can be treated as a Read
// capability — i.e. has no observable local side effect, so balanced mode can
// pass it through without prompting.
//
// The check is intentionally conservative. We don't parse shell, we don't
// open files, we don't follow PATH. Instead:
//
//  1. Any output / input redirection ( > >> < ) disqualifies the whole command.
//  2. Each segment of a compound command (split on && || ; |) must be a
//     known readonly command. One non-readonly segment poisons the lot.
//  3. Per-command flag rules trim a few known-write modes (sed -i, find
//     -exec / -delete, env CMD ...).
//  4. Leading VAR=val assignments are not even attempted — too easy to slip a
//     LD_PRELOAD-style attack through, the cost of asking is small.
//
// False negatives (treating a readonly command as non-readonly) are fine —
// the user just sees an extra ask card. False positives (auto-allowing a
// command with side effects) are NOT — they bypass user oversight in
// balanced mode entirely. When in doubt, return false.
func isReadonlyBash(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	if hasUnquotedRedirect(cmd) {
		return false
	}
	for _, seg := range splitBashSegments(cmd) {
		if !isReadonlySegment(strings.TrimSpace(seg)) {
			return false
		}
	}
	// Even if every segment is a readonly command, treating the whole thing
	// as Read would skip the dangerous-path check. `cat ~/.ssh/id_rsa` is
	// not "readonly" in the sense we want for an auto-pass — it leaks a
	// credential. Force it back to Exec so the regular ask flow runs.
	if scanBashForSensitiveRead("", cmd) != "" {
		return false
	}
	return true
}

// readonlyBashCommands lists base commands with no local side effect (when
// invoked safely — see per-command checks in isReadonlySegment for the
// caveats). awk is deliberately excluded: its `system()` function makes
// arbitrary execution trivial and the heuristic to detect it is not worth
// the risk for a small UX win.
var readonlyBashCommands = map[string]bool{
	// system/environment introspection
	"pwd":      true,
	"whoami":   true,
	"id":       true,
	"uname":    true,
	"hostname": true,
	"date":     true,
	"which":    true,
	"type":     true,
	"printenv": true,

	// file listing / metadata (no content mutation)
	"ls":       true,
	"tree":     true,
	"stat":     true,
	"file":     true,
	"basename": true,
	"dirname":  true,
	"realpath": true,
	"readlink": true,
	"df":       true,
	"du":       true,
	"wc":       true,

	// file content reads
	"cat":  true,
	"head": true,
	"tail": true,
	"less": true,
	"more": true,

	// search / filter / transform (read-only invocations only)
	"grep":    true,
	"egrep":   true,
	"fgrep":   true,
	"rg":      true,
	"ripgrep": true,
	"find":    true, // -exec / -delete checked below
	"fd":      true,
	"fdfind":  true,
	"sort":    true,
	"uniq":    true,
	"cut":     true,
	"tr":      true,
	"sed":     true, // -i / --in-place checked below
	"diff":    true,
	"cmp":     true,
	"echo":    true,
	"printf":  true,

	// git read-only subcommands (handled in subcommand check)
	"git": true,
}

// gitReadonlySubcommands gates the "git" entry above. Kept tight: only the
// handful of subcommands a coding agent actually runs constantly. Edge
// commands (rev-parse, ls-files, config --list, remote -v, branch -a) fall
// through to ask — one click to allow session covers the rest of the session.
// Smaller whitelist also means no per-flag mutation checks: any other "git X"
// just isn't readonly, simpler to reason about.
var gitReadonlySubcommands = map[string]bool{
	"status": true,
	"log":    true,
	"diff":   true,
	"show":   true,
	"blame":  true,
}

func isReadonlySegment(seg string) bool {
	tokens := strings.Fields(seg)
	if len(tokens) == 0 {
		return false
	}
	// Reject env-var prefixes outright. NODE_ENV=prod is harmless but
	// LD_PRELOAD=/tmp/evil.so is not, and we don't want to maintain a
	// whitelist of safe env names just for an auto-pass.
	if strings.ContainsRune(tokens[0], '=') {
		return false
	}
	cmd := tokens[0]
	if !readonlyBashCommands[cmd] {
		return false
	}

	switch cmd {
	case "find", "fd", "fdfind":
		for _, t := range tokens[1:] {
			switch t {
			case "-exec", "-execdir", "-delete", "-ok", "-okdir":
				return false
			}
		}
	case "sed":
		for _, t := range tokens[1:] {
			if t == "-i" || t == "--in-place" || strings.HasPrefix(t, "-i") {
				return false
			}
		}
	case "git":
		// "git" alone (help screen) is fine; otherwise the subcommand must
		// be in the tight readonly whitelist.
		if len(tokens) < 2 {
			return true
		}
		if !gitReadonlySubcommands[tokens[1]] {
			return false
		}
	}
	return true
}

// hasUnquotedRedirect scans for shell redirection / process-substitution
// characters outside quotes. Any `<` or `>` disqualifies the command from
// the readonly fast-path. We do NOT try to distinguish `2>&1` (technically
// stderr-to-stdout, no write) from `> file`; the former is rare enough in
// readonly commands that an extra ask is fine.
func hasUnquotedRedirect(cmd string) bool {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if inSingle || inDouble {
			continue
		}
		if ch == '>' || ch == '<' {
			return true
		}
	}
	return false
}

// splitBashSegments splits a shell command on unquoted &&, ||, ;, | into
// individual segments. Mirrors agentcore/permission/rules.go:splitShellSegments
// (kept private there); duplicated here to avoid widening that package's API
// surface for a single caller.
func splitBashSegments(cmd string) []string {
	var (
		parts    []string
		buf      strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)
	flush := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			parts = append(parts, s)
		}
		buf.Reset()
	}
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			buf.WriteByte(ch)
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			buf.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			buf.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble {
			if ch == ';' {
				flush()
				continue
			}
			if i+1 < len(cmd) && ((ch == '&' && cmd[i+1] == '&') || (ch == '|' && cmd[i+1] == '|')) {
				flush()
				i++
				continue
			}
			if ch == '|' {
				flush()
				continue
			}
		}
		buf.WriteByte(ch)
	}
	flush()
	return parts
}

// bashPrefix returns a stable key suffix for store / sessionAllow bucketing.
//
// "git commit -m 'fix x'"  → "git commit"   (next commit reuses the entry)
// "ls -la /tmp"            → "ls"
// "NODE_ENV=prod npm run build" → "npm run" (env var skipped)
// "" or fully-malformed     → "" (caller picks fallback)
//
// This replaces the previous SHA-hash key — those bucketed every command
// argument variant separately, so users had to re-approve `git commit -m "y"`
// after approving `git commit -m "x"`.
func bashPrefix(cmd string) string {
	segs := splitBashSegments(cmd)
	if len(segs) == 0 {
		return ""
	}
	// Use the FIRST segment of compound commands as the prefix. Users almost
	// always reason about "the command they typed", and `a && b` reuses
	// `a` as its identifier in any reasonable allow rule.
	tokens := strings.Fields(segs[0])
	i := 0
	for i < len(tokens) && envVarAssignRE.MatchString(tokens[i]) {
		i++
	}
	if i >= len(tokens) {
		return ""
	}
	first := tokens[i]
	// Only attempt subcommand extraction when the first token itself looks
	// like a standard command name (lowercase alnum). Skip for paths
	// (./script.sh), flags, absolute exec (/usr/bin/python), etc. — there
	// the prefix IS the first token alone.
	if !subcommandShapeRE.MatchString(first) {
		return first
	}
	if i+1 < len(tokens) && subcommandShapeRE.MatchString(tokens[i+1]) {
		return first + " " + tokens[i+1]
	}
	return first
}

var (
	envVarAssignRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	subcommandShapeRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)
