package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

func (s *Sandbox) start() error {
	tool, err := exec.LookPath("bwrap")
	if err != nil {
		return errors.New("bubblewrap (bwrap) is not installed")
	}
	tmp, err := os.MkdirTemp("", "codebot-sandbox-")
	if err != nil {
		return err
	}
	s.tool, s.tmp = tool, tmp
	if out, err := exec.Command(tool, append(s.base(), "--", "true")...).CombinedOutput(); err != nil {
		s.Close()
		return fmt.Errorf("bwrap does not work here: %s", strings.TrimSpace(err.Error()+" "+string(out)))
	}
	return nil
}

// base is a read-only view of the system with a private /tmp, which keeps
// the X11 and SSH agent sockets out, and without the network, whose own
// namespace still has a loopback.
func (s *Sandbox) base() []string {
	args := []string{"--die-with-parent", "--new-session", "--unshare-pid",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
		"--bind", s.tmp, "/tmp", "--setenv", "TMPDIR", "/tmp"}
	if !s.network {
		args = append(args, "--unshare-net")
	}
	return args
}

// wrap mounts the writable directories, then the protected paths over them,
// each kind outer before inner, as a mount covers those made before it
// inside it. A guard in a writable directory is mounted onto itself: a
// mount point cannot be moved. A protected path that does not exist is not
// covered, as mounting over it would create it.
func (s *Sandbox) wrap(argv []string, p policy) []string {
	args := append([]string{s.tool}, s.base()...)
	writable := slices.Clone(p.write)
	for _, dir := range p.guard {
		if slices.ContainsFunc(p.write, func(w string) bool { return holds(w, dir) }) {
			writable = append(writable, dir)
		}
	}
	for _, dir := range byDepth(writable) {
		if exists(dir) {
			args = append(args, "--bind", dir, dir)
		}
	}
	for _, path := range byDepth(p.protect) {
		if exists(path) {
			args = append(args, "--ro-bind", path, path)
		}
	}
	for _, path := range p.credentials {
		args = hide(args, path)
	}
	// Services that would run a command outside: the user's session bus and
	// container engines.
	for _, path := range []string{fmt.Sprintf("/run/user/%d", os.Getuid()), "/run/docker.sock", "/var/run/docker.sock", "/run/podman/podman.sock", "/run/containerd/containerd.sock"} {
		args = hide(args, path)
	}
	return append(append(args, "--"), argv...)
}

func byDepth(paths []string) []string {
	paths = uniq(paths)
	slices.SortStableFunc(paths, func(a, b string) int { return strings.Count(a, "/") - strings.Count(b, "/") })
	return paths
}

// hide covers a directory with an empty one and a file with /dev/null.
func hide(args []string, path string) []string {
	fi, err := os.Stat(path)
	switch {
	case err != nil:
		return args
	case fi.IsDir():
		return append(args, "--tmpfs", path)
	default:
		return append(args, "--ro-bind", "/dev/null", path)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
