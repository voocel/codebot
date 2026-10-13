package permission

import (
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// script is what a shell line runs, read as bash would read it, without
// running it.
type script struct {
	cmds []command
	// opaque is set when the line may do more than cmds show: it expands a
	// variable or a command, runs one in the background or behind variable
	// assignments, redirects to or from a file, or uses a compound command,
	// such as a loop, a function or a subshell.
	opaque bool
}

// command is a simple command: its words with the quotes removed, and of
// each the part before an unquoted glob character (see beforeGlob), all of
// it if none.
type command struct {
	args    []string
	literal []string
}

// looseGlob reports whether an argument is a glob that may expand to an
// option, as * does to a file named -rf.
func (c command) looseGlob() bool {
	for i, lit := range c.literal[1:] {
		if lit != c.args[i+1] && (lit == "" || lit[0] == '-') {
			return true
		}
	}
	return false
}

func parse(line string) (*syntax.File, error) {
	return syntax.NewParser().Parse(strings.NewReader(line), "")
}

func parseScript(line string) script {
	file, err := parse(line)
	if err != nil {
		return script{opaque: true}
	}
	var s script
	syntax.Walk(file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case nil: // the end of a node's children
		case *syntax.Stmt:
			if n.Background || n.Coprocess || n.Disown || slices.ContainsFunc(n.Redirs, writesOrReads) {
				s.opaque = true
			}
		case *syntax.File, *syntax.Redirect, *syntax.BinaryCmd, *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted:
		case *syntax.CallExpr:
			if len(n.Assigns) > 0 || len(n.Args) == 0 {
				s.opaque = true
				break
			}
			if c, ok := literalCommand(n.Args); ok {
				s.cmds = append(s.cmds, c)
			} else {
				s.opaque = true
			}
		default:
			s.opaque = true
		}
		return !s.opaque
	})
	return s
}

// writesOrReads reports a redirection to or from a file. A here-document
// and duplicating a descriptor are neither, nor is writing to /dev/null.
func writesOrReads(r *syntax.Redirect) bool {
	switch r.Op {
	case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return false
	case syntax.DplIn, syntax.DplOut:
		return r.Word.Lit() != "-" && strings.Trim(r.Word.Lit(), "0123456789") != ""
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll:
		return r.Word.Lit() != "/dev/null"
	}
	return true
}

// literalCommand returns the command of words, if quote removal alone
// makes it: no expansion, and no glob in the command name.
func literalCommand(words []*syntax.Word) (command, bool) {
	var c command
	for i, w := range words {
		fields, ok := text(w)
		if !ok || len(fields) != 1 {
			// An expansion, or a brace one that makes several words.
			return command{}, false
		}
		lit, glob := beforeGlob(w)
		if glob && i == 0 {
			return command{}, false
		}
		if !glob {
			lit = fields[0]
		}
		c.args = append(c.args, fields[0])
		c.literal = append(c.literal, lit)
	}
	return c, true
}

// text returns the words w makes, if it is text alone: quoted, unquoted or
// in braces, but with nothing to expand.
func text(w *syntax.Word) ([]string, bool) {
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					return nil, false
				}
			}
		default:
			return nil, false
		}
	}
	fields, err := expand.Fields(nil, w)
	return fields, err == nil
}

// beforeGlob returns the text of w before its first unquoted glob
// character, and whether it has one. A glob right after a lone dot, as in
// .*, matches .. in bash before 5.2, macOS's among them, so there the dot
// reads "..": the glob may reach that directory's parent.
func beforeGlob(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			for i := 0; i < len(p.Value); i++ {
				switch c := p.Value[i]; c {
				case '\\':
					if i++; i < len(p.Value) {
						b.WriteByte(p.Value[i])
					}
				case '*', '?', '[':
					s := b.String()
					if dir, last := path.Split(s); last == "." {
						s = dir + ".."
					}
					return s, true
				default:
					b.WriteByte(c)
				}
			}
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if lit, ok := q.(*syntax.Lit); ok {
					b.WriteString(lit.Value)
				}
			}
		}
	}
	return b.String(), false
}

// eachCommand calls f with each command line may run, as far as reading it
// tells, until f returns true: its simple commands, nested ones included;
// for one that runs another, such as sudo or find -exec, each run of its
// arguments; and the commands of a script given to sh -c or eval. A word
// whose value only running tells is "".
func eachCommand(line string, f func(args []string) bool) bool {
	file, err := parse(line)
	if err != nil {
		// bash runs what precedes a syntax error, so take the words as
		// they come.
		for _, seg := range strings.FieldsFunc(line, func(r rune) bool { return strings.ContainsRune(";&|\n()`$<>", r) }) {
			if args := strings.Fields(strings.NewReplacer(`"`, "", `'`, "", `\`, "").Replace(seg)); len(args) > 0 && runs(args, f) {
				return true
			}
		}
		return false
	}
	found := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			args := make([]string, len(call.Args))
			for i, w := range call.Args {
				if fields, ok := text(w); ok {
					args[i] = strings.Join(fields, " ")
				}
			}
			found = runs(args, f)
		}
		return !found
	})
	return found
}

// runs calls f with args and the commands args runs.
func runs(args []string, f func([]string) bool) bool {
	cmds := [][]string{args}
	// A command named by an expansion may be any.
	if args[0] == "" || runsAnother[path.Base(args[0])] {
		for i := 1; i < len(args); i++ {
			cmds = append(cmds, args[i:])
		}
	}
	for _, cmd := range cmds {
		if f(cmd) {
			return true
		}
		switch name := path.Base(cmd[0]); {
		case name == "eval":
			if eachCommand(strings.Join(cmd[1:], " "), f) {
				return true
			}
		case shells[name]:
			if i := slices.Index(cmd, "-c"); i > 0 && i+1 < len(cmd) && eachCommand(cmd[i+1], f) {
				return true
			}
		}
	}
	return false
}

var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ksh": true}

// The commands in runsAnother run a command they are given, so remembering
// one would allow anything.
var runsAnother = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"eval": true, "exec": true, "source": true, ".": true, "command": true, "builtin": true,
	"sudo": true, "su": true, "doas": true, "env": true, "xargs": true, "nohup": true,
	"nice": true, "timeout": true, "time": true, "watch": true, "chroot": true,
	"find": true, "fd": true, "fdfind": true,
	"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "php": true,
	"deno": true, "bun": true, "npx": true, "bunx": true, "pnpx": true, "uvx": true,
}
