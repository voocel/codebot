package sandbox

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

//go:embed seatbelt.sbpl
var seatbelt string

//go:embed seatbelt_network.sbpl
var seatbeltNetwork string

// sandboxExec is not looked up in PATH, which a command could change.
const sandboxExec = "/usr/bin/sandbox-exec"

func (s *Sandbox) start() error {
	s.tool = sandboxExec
	if out, err := exec.Command(sandboxExec, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
		return fmt.Errorf("%s does not work here: %s", sandboxExec, strings.TrimSpace(err.Error()+" "+string(out)))
	}
	return nil
}

// wrap builds a Seatbelt profile: closed by default, then file access, where
// a later rule overrides an earlier one, then the network.
func (s *Sandbox) wrap(argv []string, p policy) []string {
	var b strings.Builder
	b.WriteString(seatbelt)
	b.WriteString("\n(allow file-read*)\n")
	rule(&b, "allow file-write*", "subpath", slices.Concat(p.write, []string{resolved(os.TempDir()), "/private/tmp"}))
	rule(&b, "deny file-write*", "subpath", p.protect)
	// Moving a directory unlinks it.
	rule(&b, "deny file-write-unlink", "literal", p.guard)
	rule(&b, "deny file-read* file-write*", "subpath", p.credentials)
	if s.network {
		b.WriteString(seatbeltNetwork)
		// IP only: a Unix socket such as Docker's would run things outside.
		b.WriteString(`(allow network-outbound (remote ip "*:*"))
(allow network-inbound (local ip "*:*"))
(allow network-bind (local ip "*:*"))
(allow network-outbound (remote unix-socket (path-literal "/private/var/run/mDNSResponder")))
`)
	}
	b.WriteString(`(deny mach-lookup (xpc-service-name-prefix ""))
; These fcntls write through read-only descriptors.
(deny system-fcntl (fcntl-command 80 110))
`)
	return append([]string{s.tool, "-p", b.String()}, argv...)
}

// rule writes (action (match path)...). It writes nothing for no paths: a
// rule without any would cover every path.
func rule(b *strings.Builder, action, match string, paths []string) {
	if len(paths) == 0 {
		return
	}
	b.WriteString("(" + action)
	for _, p := range paths {
		fmt.Fprintf(b, " (%s %s)", match, quote(p))
	}
	b.WriteString(")\n")
}

func quote(p string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}
