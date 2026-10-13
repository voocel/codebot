// Package sandbox runs shell commands inside an OS sandbox: Seatbelt on
// macOS, bubblewrap on Linux. A sandboxed command reads what the user can,
// except credentials. It writes only in the directories it is given and the
// temp directory, and not even there to the paths it is told to protect.
// Unless the network is allowed it reaches no network, the loopback
// included, where a local proxy would lead out.
package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
)

type Sandbox struct {
	network bool
	home    string
	tool    string // the sandbox program
	tmp     string // Linux: the directory mounted over /tmp
}

// New checks that the sandbox works here, so that no command runs
// unsandboxed for want of it.
func New(network bool) (*Sandbox, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	s := &Sandbox{network: network, home: resolved(home)}
	if err := s.start(); err != nil {
		return nil, err
	}
	return s, nil
}

// Network reports whether sandboxed commands reach the network.
func (s *Sandbox) Network() bool { return s.network }

func (s *Sandbox) Close() error {
	if s.tmp == "" {
		return nil
	}
	return os.RemoveAll(s.tmp)
}

// policy is what a sandboxed command may do. A path is given both as it is
// and resolved, so that a symlink and what it points to are covered alike.
type policy struct {
	write   []string // writable directories
	protect []string // read-only paths in them
	// guard are the directories above a protected path, which must not be
	// moved: that would carry the path out from under its protection, or
	// let another take its place.
	guard       []string
	credentials []string // neither readable nor writable
}

// Wrap returns the command that runs argv in the sandbox, able to write in
// writable but not in protected. A directory holding the home directory is
// not writable: what in there runs later is too much to protect by name.
func (s *Sandbox) Wrap(argv, writable, protected []string) []string {
	var p policy
	for _, dir := range writable {
		if dir = resolved(dir); !holds(dir, s.home) {
			p.write = append(p.write, dir)
		}
	}
	for _, path := range protected {
		for _, v := range variants(path) {
			if slices.ContainsFunc(p.write, func(dir string) bool { return holds(dir, v) }) {
				p.protect = append(p.protect, v)
			}
		}
	}
	for _, path := range p.protect {
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			p.guard = append(p.guard, dir)
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	for _, path := range s.credentials() {
		p.credentials = append(p.credentials, variants(path)...)
	}
	p.write, p.protect, p.guard = uniq(p.write), uniq(p.protect), uniq(p.guard)
	return s.wrap(argv, p)
}

// credentials are the files that hold the user's keys and tokens.
func (s *Sandbox) credentials() []string {
	var out []string
	for _, p := range []string{
		".ssh", ".aws", ".azure", ".gnupg", ".kube", ".docker", ".config/gcloud", ".config/gh",
		".netrc", ".git-credentials", ".pgpass", ".npmrc", ".pypirc", ".cargo/credentials.toml",
		".bash_history", ".zsh_history", ".claude/.credentials.json", ".codex/auth.json",
	} {
		out = append(out, filepath.Join(s.home, p))
	}
	dir := config.UserConfigDir()
	return append(out, filepath.Join(dir, "settings.json"), filepath.Join(dir, "mcp-oauth.json"))
}

// holds reports whether path is dir or in it.
func holds(dir, path string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// variants returns the path p names, with the symlinks above it resolved,
// and, if it is a symlink, the path it points to.
func variants(p string) []string {
	p = filepath.Clean(p)
	return slices.Compact([]string{filepath.Join(resolved(filepath.Dir(p)), filepath.Base(p)), resolved(p)})
}

// resolved follows symlinks, as the sandbox does, through the part of p that
// exists.
func resolved(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if parent := filepath.Dir(p); parent != p {
		return filepath.Join(resolved(parent), filepath.Base(p))
	}
	return p
}

func uniq(paths []string) []string {
	slices.Sort(paths)
	return slices.Compact(paths)
}
