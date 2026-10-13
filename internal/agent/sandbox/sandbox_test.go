package sandbox

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// dir makes a directory outside the temp directory, which the sandbox lets
// commands write to (and on Linux replaces).
func dir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp(".", ".sandbox-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	abs, err := filepath.Abs(d)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func newSandbox(t *testing.T, network bool) *Sandbox {
	t.Helper()
	t.Setenv("HOME", dir(t))
	s, err := New(network)
	if err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// sh runs script in the sandbox and reports whether it succeeded.
func sh(t *testing.T, s *Sandbox, writable, protected []string, script string) (string, bool) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	argv := s.Wrap([]string{bash, "-c", script}, writable, protected)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestWrites(t *testing.T) {
	s := newSandbox(t, false)
	ws, outside := dir(t), dir(t)
	mkdir(t, filepath.Join(ws, ".git", "hooks"))
	if err := os.WriteFile(filepath.Join(ws, ".bashrc"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	protected := []string{filepath.Join(ws, ".git"), filepath.Join(ws, ".bashrc"), filepath.Join(ws, ".vscode")}

	for _, c := range []struct {
		script string
		ok     bool
	}{
		{"echo x > a && mkdir -p src && echo y > src/b", true},
		{"echo x > \"$TMPDIR\"/sandbox-test && echo x > /tmp/sandbox-test", true},
		{"echo x > " + outside + "/a", false},
		{"echo x > .git/hooks/pre-commit", false},
		{"echo x > .git/commondir", false},
		{"mv .git moved", false},
		{"echo x > .bashrc", false},
	} {
		if out, ok := sh(t, s, []string{ws}, protected, "cd "+ws+" && "+c.script); ok != c.ok {
			t.Errorf("%s: ok = %v, want %v: %s", c.script, ok, c.ok, out)
		}
	}
	// Seatbelt matches paths, so it also keeps protected ones from being
	// created; bubblewrap can only cover what exists.
	if runtime.GOOS == "darwin" {
		if out, ok := sh(t, s, []string{ws}, protected, "mkdir "+ws+"/.vscode"); ok {
			t.Errorf("created .vscode: %s", out)
		}
	}
}

// A protected symlink protects what it points to, and on macOS itself.
func TestProtectedSymlink(t *testing.T) {
	s := newSandbox(t, false)
	ws := dir(t)
	mkdir(t, filepath.Join(ws, "shared"))
	if err := os.Symlink("shared", filepath.Join(ws, ".vscode")); err != nil {
		t.Fatal(err)
	}
	protected := []string{filepath.Join(ws, ".vscode")}
	if out, ok := sh(t, s, []string{ws}, protected, "echo x > "+ws+"/.vscode/tasks.json"); ok {
		t.Errorf("wrote through the symlink: %s", out)
	}
	if runtime.GOOS == "darwin" {
		if out, ok := sh(t, s, []string{ws}, protected, "cd "+ws+" && rm .vscode && mkdir .vscode"); ok {
			t.Errorf("replaced the symlink: %s", out)
		}
	}
}

// No directory above a protected path moves, a writable one included, so
// none can carry the path off or put another in its place.
func TestGuards(t *testing.T) {
	s := newSandbox(t, false)
	parent := dir(t)
	ws := filepath.Join(parent, "a", "ws")
	mkdir(t, filepath.Join(ws, ".git"))
	writable, protected := []string{parent, ws}, []string{filepath.Join(ws, ".git")}
	for _, script := range []string{"mv " + ws + " " + parent + "/moved", "mv " + parent + "/a " + parent + "/b"} {
		if out, ok := sh(t, s, writable, protected, script); ok {
			t.Errorf("%s: %s", script, out)
		}
	}
	if out, ok := sh(t, s, writable, protected, "mkdir "+parent+"/c && mv "+parent+"/c "+parent+"/d"); !ok {
		t.Errorf("could not move a directory holding nothing protected: %s", out)
	}
}

// What in the home directory runs later is too much to protect by name, so
// a writable directory holding it is not writable.
func TestHomeIsReadOnly(t *testing.T) {
	s := newSandbox(t, false)
	home := os.Getenv("HOME")
	project := filepath.Join(home, "project")
	mkdir(t, project)
	if out, ok := sh(t, s, []string{home}, nil, "echo x > "+home+"/.zshenv"); ok {
		t.Errorf("wrote in the home directory: %s", out)
	}
	if out, ok := sh(t, s, []string{project}, nil, "echo x > "+project+"/a"); !ok {
		t.Errorf("could not write in a project at home: %s", out)
	}
}

// A protected directory may be a writable one too, such as a local plugin
// given as a write root: protection wins.
func TestProtectedRoot(t *testing.T) {
	s := newSandbox(t, false)
	plugin := dir(t)
	if out, ok := sh(t, s, []string{plugin}, []string{plugin}, "echo x > "+plugin+"/run"); ok {
		t.Errorf("wrote into the protected directory: %s", out)
	}
}

func TestReadsAllButCredentials(t *testing.T) {
	s := newSandbox(t, false)
	home := os.Getenv("HOME")
	mkdir(t, filepath.Join(home, ".ssh"))
	for name, body := range map[string]string{".ssh/id_ed25519": "key", ".netrc": "token", "notes": "plain"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, ok := sh(t, s, nil, nil, "cat ~/notes"); !ok || out != "plain" {
		t.Errorf("read a plain file: %q, %v", out, ok)
	}
	for _, name := range []string{".ssh/id_ed25519", ".netrc"} {
		if out, _ := sh(t, s, nil, nil, "cat ~/"+name); strings.Contains(out, "key") || strings.Contains(out, "token") {
			t.Errorf("read %s: %q", name, out)
		}
	}
}

// Without the network a command reaches not even the loopback, where a
// local proxy would lead out.
func TestNetwork(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	connect := "exec 3<>/dev/tcp/127.0.0.1/" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)

	if out, ok := sh(t, newSandbox(t, false), nil, nil, connect); ok {
		t.Errorf("reached the loopback without the network: %s", out)
	}
	if out, ok := sh(t, newSandbox(t, true), nil, nil, connect); !ok {
		t.Errorf("did not reach the loopback with the network: %s", out)
	}
}
