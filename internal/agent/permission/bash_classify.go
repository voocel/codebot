package permission

import (
	"regexp"
	"slices"
	"strings"
)

// isReadonlyBash reports whether balanced mode may run cmd without asking.
// It is deliberately conservative and does not parse shell: a redirect from
// or to a file disqualifies cmd, and every segment must be a known read-only
// command. A false negative costs one prompt; a false positive skips the
// user entirely, so when in doubt it returns false.
func isReadonlyBash(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	if hasUnquotedInput(cmd) || opaque(cmd) {
		return false
	}
	for _, seg := range splitBashSegments(cmd) {
		if !isReadonlySegment(strings.TrimSpace(seg)) {
			return false
		}
	}
	// cat ~/.ssh/id_rsa reads, but leaks a credential; keep it on the
	// regular ask flow.
	if scanBashForSensitiveRead("", cmd) != "" {
		return false
	}
	return true
}

// readonlyBashCommands have no local side effect when invoked safely; see
// isReadonlySegment for the per-command checks. awk is excluded because
// system() makes arbitrary execution trivial.
var readonlyBashCommands = map[string]bool{
	"pwd":      true,
	"whoami":   true,
	"id":       true,
	"uname":    true,
	"hostname": true,
	"date":     true,
	"which":    true,
	"type":     true,
	"printenv": true,

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

	"cat":  true,
	"head": true,
	"tail": true,
	"less": true,
	"more": true,

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

	"git": true,
}

// gitReadonlySubcommands is kept small on purpose: any other git subcommand
// asks, which spares per-flag mutation checks.
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
	// NODE_ENV=prod is harmless but LD_PRELOAD=/tmp/evil.so is not; reject
	// every assignment rather than keep a list of safe names.
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
		// Bare "git" only prints help.
		if len(tokens) < 2 {
			return true
		}
		if !gitReadonlySubcommands[tokens[1]] {
			return false
		}
	}
	return true
}

// hasUnquotedInput reports a < outside quotes: input from a file may read a
// credential the path scan misses, as <~/.ssh/id_rsa does. Output redirects
// are opaque's to judge, which lets 2>&1 and 2>/dev/null through.
func hasUnquotedInput(cmd string) bool {
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
		if ch == '<' {
			return true
		}
	}
	return false
}

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

// commandKeys returns an approval key ("exec:go test") for each command in
// cmd that is not read-only. It returns nil, so nothing is remembered, when
// cmd is opaque or destructive, or when a command is prefixed by variables
// or runs another command.
func commandKeys(cmd string) []string {
	if opaque(cmd) || destructiveCommandWarning(cmd) != "" {
		return nil
	}
	var keys []string
	for _, seg := range splitBashSegments(cmd) {
		if isReadonlySegment(seg) {
			continue
		}
		p := commandPrefix(seg)
		if p == "" {
			return nil
		}
		if k := "exec:" + p; !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// commandPrefix is what an approval remembers: "git commit -m x" → "git
// commit", "./script.sh arg" → "./script.sh". It is "" for a command behind
// variable assignments (LD_PRELOAD=…) or one that runs another (sudo, sh).
func commandPrefix(seg string) string {
	tokens := strings.Fields(seg)
	if len(tokens) == 0 || envVarAssignRE.MatchString(tokens[0]) || runsAnother[tokens[0]] {
		return ""
	}
	// A subcommand only follows a plain command name, not a path or a flag.
	if subcommandShapeRE.MatchString(tokens[0]) && len(tokens) > 1 && subcommandShapeRE.MatchString(tokens[1]) {
		return tokens[0] + " " + tokens[1]
	}
	return tokens[0]
}

// The commands in runsAnother run a command they are given, so remembering
// one would allow anything.
var runsAnother = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"eval": true, "exec": true, "source": true, ".": true, "command": true, "builtin": true,
	"sudo": true, "su": true, "doas": true, "env": true, "xargs": true, "nohup": true,
	"nice": true, "timeout": true, "time": true, "watch": true, "chroot": true,
}

// opaque reports whether cmd may do more than its commands show: command
// substitution, background jobs, extra lines or redirection to a file. Only
// single quotes disarm these; substitution still runs inside double quotes.
func opaque(cmd string) bool {
	single, double := false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		next := byte(0)
		if i+1 < len(cmd) {
			next = cmd[i+1]
		}
		switch {
		case single:
			single = c != '\''
		case c == '\\':
			i++
		case c == '\'' && !double:
			single = true
		case c == '"':
			double = !double
		case c == '`', c == '$' && next == '(':
			return true
		case double:
		case c == '\n', (c == '<' || c == '>') && next == '(':
			return true
		case c == '&' && next == '&':
			i++
		case c == '&':
			// The & in 2>&1 or &> is a redirection, not a background job.
			if next != '>' && (i == 0 || cmd[i-1] != '>') {
				return true
			}
		case c == '>':
			if writesFile(cmd[i+1:]) {
				return true
			}
		}
	}
	return false
}

// writesFile reports whether the redirect target is a file, not &N or
// /dev/null.
func writesFile(rest string) bool {
	rest = strings.TrimLeft(strings.TrimPrefix(rest, ">"), " \t")
	if strings.HasPrefix(rest, "&") {
		return false
	}
	target, _, _ := strings.Cut(rest, " ")
	return strings.TrimRight(target, ";|&") != "/dev/null"
}

var (
	envVarAssignRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	subcommandShapeRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)
