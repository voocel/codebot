package regular

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(file); err != nil || string(data) != "x" {
		t.Fatalf("%q, %v", data, err)
	}
	if _, err := ReadFile(dir); err == nil {
		t.Error("read a directory")
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(dir, "zero")
	if err := os.Symlink("/dev/zero", link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(link); err == nil {
		t.Error("read a device")
	}
}

func TestWithin(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFileIn(root, filepath.Join(root, "f")); err != nil || string(data) != "x" {
		t.Fatalf("%q, %v", data, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for name, target := range map[string]string{"in": "f", "out": secret, "up": "../" + filepath.Base(outside) + "/secret"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := ReadFileIn(root, filepath.Join(root, "in")); err != nil || string(data) != "x" {
		t.Errorf("a link within: %q, %v", data, err)
	}
	for _, name := range []string{"out", "up"} {
		if data, err := ReadFileIn(root, filepath.Join(root, name)); err == nil {
			t.Errorf("read %s through %s", data, name)
		}
	}
	// A directory leading out leads its files out.
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if _, err := Within(root, filepath.Join(root, "dir", "secret")); err == nil {
		t.Error("a file under a directory leading out stayed within")
	}
	// Everything is within the filesystem's root.
	if _, err := Within("/", secret); err != nil {
		t.Errorf("within /: %v", err)
	}
}
