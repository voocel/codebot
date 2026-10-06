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
