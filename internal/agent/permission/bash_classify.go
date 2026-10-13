package permission

import (
	"cmp"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

// shellLine is what the engine makes of a bash command.
type shellLine struct {
	// readonly is set when every command only reads, so that balanced mode
	// may run the line without asking. It is conservative: a false negative
	// costs one prompt, a false positive skips the user entirely.
	readonly bool
	// reads are the files the read-only commands read, which must lie in
	// the readable roots for the line to run unasked.
	reads []string
	// keys are what Allow Always remembers ("exec:go test"), one for each
	// command that is not read-only; nil when nothing can be remembered, as
	// for a line that is opaque or destructive, or a command that runs
	// another.
	keys []string
}

func readShell(line string) shellLine {
	s := parseScript(line)
	if s.opaque || len(s.cmds) == 0 {
		return shellLine{}
	}
	out := shellLine{readonly: true}
	remember := destructiveCommandWarning(line) == ""
	for _, c := range s.cmds {
		if reads, ok := readonly(c); ok {
			out.reads = append(out.reads, reads...)
			continue
		}
		out.readonly = false
		p := commandPrefix(c.args)
		if p == "" {
			remember = false
		} else if k := "exec:" + p; !slices.Contains(out.keys, k) {
			out.keys = append(out.keys, k)
		}
	}
	if !remember {
		out.keys = nil
	}
	return out
}

// readonlySpec describes a command with no side effect.
type readonlySpec struct {
	// files marks a command whose operands are files it reads.
	files bool
	// pattern marks one whose first operand is a pattern, unless options
	// give it.
	pattern bool
	// allows, if set, checks the arguments for options that write or run a
	// program. A command with one may not take a glob that could expand to
	// an option.
	allows func(args []string) bool
}

// readonlyCommands have no side effect, given arguments their spec allows.
// awk is excluded because system() makes running anything trivial, less and
// more because LESSOPEN runs a program.
var readonlyCommands = map[string]readonlySpec{
	"pwd": {}, "whoami": {}, "id": {}, "uname": {}, "which": {}, "type": {},
	"echo": {}, "printf": {}, "basename": {}, "dirname": {}, "tr": {},
	"hostname": {allows: optionsOnly}, // an operand sets the name
	"date":     {allows: without("s", "--set")},

	"ls": {files: true}, "stat": {files: true}, "realpath": {files: true}, "readlink": {files: true},
	"df": {files: true}, "du": {files: true}, "wc": {files: true},
	"cat": {files: true}, "head": {files: true}, "tail": {files: true},
	"grep": {files: true, pattern: true}, "egrep": {files: true, pattern: true}, "fgrep": {files: true, pattern: true},
	"cut": {files: true}, "diff": {files: true}, "cmp": {files: true},

	"tree": {files: true, allows: without("oR")},
	"file": {files: true, allows: without("C", "--compile")},
	// --files0-from reads the files another file names.
	"sort": {files: true, allows: without("o", "--output", "--compress-program", "--files0-from")},
	"uniq": {files: true, allows: func(args []string) bool { return len(operands(args)) <= 1 }}, // a second operand is written
	"rg":   {files: true, pattern: true, allows: without("", "--pre", "--hostname-bin")},
	"find": {files: true, allows: func(args []string) bool {
		return !slices.ContainsFunc(args, func(a string) bool {
			return slices.Contains([]string{"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls"}, a)
		})
	}},
	"sed": {files: true, pattern: true, allows: sedPrints},
	// git's other subcommands may change the repository; these may write a
	// file or run a program only as their options ask.
	"git": {files: true, allows: func(args []string) bool {
		if len(args) == 0 {
			return true
		}
		switch args[0] {
		case "status", "log", "diff", "show", "blame":
			return without("", "--output", "--ext-diff", "--textconv")(args)
		}
		return false
	}},
}

// readonly returns the files c reads, if it only reads.
func readonly(c command) ([]string, bool) {
	spec, ok := readonlyCommands[c.args[0]]
	if !ok || spec.allows != nil && (!spec.allows(c.args[1:]) || c.looseGlob()) {
		return nil, false
	}
	if !spec.files {
		return nil, true
	}
	ops := operands(c.literal[1:])
	if spec.pattern && len(ops) > 0 && !patternGiven(c.args[1:]) {
		ops = ops[1:]
	}
	if len(ops) == 0 {
		ops = []string{"."} // the working directory, as ls and grep -r read, or standard input
	}
	var reads []string
	for _, op := range ops {
		switch {
		case op == "~" || strings.HasPrefix(op, "~/"):
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, false
			}
			op = home + op[1:]
		case strings.HasPrefix(op, "~"):
			return nil, false // another user's home
		}
		reads = append(reads, cmp.Or(op, "."))
	}
	return reads, true
}

// operands returns the arguments that are not options, the values of
// options included, and the values of those written --name=value; "-" is
// standard input. After "--" all are operands.
func operands(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--" {
			return append(out, args[i+1:]...)
		}
		if name, value, ok := strings.Cut(a, "="); ok && strings.HasPrefix(name, "--") {
			out = append(out, value)
		} else if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

// patternGiven reports whether options give grep, rg or sed its pattern or
// script, with -e or -f, alone or grouped, or a long form of them.
func patternGiven(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool {
		name, _, _ := strings.Cut(a, "=")
		switch {
		case name == "--regexp" || name == "--file" || name == "--expression":
			return true
		case strings.HasPrefix(a, "--"):
			return false
		}
		return strings.HasPrefix(a, "-") && strings.ContainsAny(a[1:], "ef")
	})
}

// without returns a check that refuses the short options in letters, alone
// or grouped as in -uo, and the long ones, given as --name or --name=value.
func without(letters string, long ...string) func([]string) bool {
	return func(args []string) bool {
		for _, a := range args {
			if a == "--" {
				return true
			}
			if strings.HasPrefix(a, "--") {
				name, _, _ := strings.Cut(a, "=")
				if slices.Contains(long, name) {
					return false
				}
			} else if strings.HasPrefix(a, "-") && strings.ContainsAny(a[1:], letters) {
				return false
			}
		}
		return true
	}
}

func optionsOnly(args []string) bool { return len(operands(args)) == 0 }

// sedScript matches scripts that only print or delete lines by number or
// pattern, such as 1,80p: sed's other commands may write files (w) or run
// programs (e).
var sedScript = func() *regexp.Regexp {
	addr := `(?:\d+|\$|/[^/\\\n]*/)`
	cmd := `(?:` + addr + `(?:,(?:` + addr + `|\+\d+))?)?!?[pdq=]`
	return regexp.MustCompile(`^\s*` + cmd + `(?:\s*[;\n]\s*` + cmd + `)*\s*;?\s*$`)
}()

func sedPrints(args []string) bool {
	script := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-e" || a == "--expression":
			if i++; i == len(args) || !sedScript.MatchString(args[i]) {
				return false
			}
			script = true
		case slices.Contains([]string{"-n", "--quiet", "--silent", "-E", "-r", "--regexp-extended", "-s", "--separate", "-u", "--unbuffered", "-z", "--null-data"}, a):
		case strings.HasPrefix(a, "-"):
			return false // -i edits in place, -f reads a script
		case !script:
			if !sedScript.MatchString(a) {
				return false
			}
			script = true
		}
	}
	return true
}

// commandPrefix is what an approval remembers: "git commit -m x" → "git
// commit", "./script.sh arg" → "./script.sh". It is "" for a command that
// runs another, and for git with options before its subcommand, which can
// make it run anything (-c core.sshCommand=…).
func commandPrefix(args []string) string {
	if runsAnother[path.Base(args[0])] || args[0] == "git" && len(args) > 1 && strings.HasPrefix(args[1], "-") {
		return ""
	}
	// A subcommand only follows a plain command name, not a path or a flag.
	if subcommandShapeRE.MatchString(args[0]) && len(args) > 1 && subcommandShapeRE.MatchString(args[1]) {
		return args[0] + " " + args[1]
	}
	return args[0]
}

var subcommandShapeRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
