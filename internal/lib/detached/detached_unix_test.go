//go:build !windows

package detached

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A canceled command returns at once and its children die too, even though
// they hold its output.
func TestCancelKillsWhatItStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Command(ctx, "sh", "-c", `sleep 30 & echo $! > "$0"; sleep 30`, pidFile).Output(); err == nil {
		t.Fatal("a cancelled command succeeded")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the command took %s to give up", took)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// The orphaned child is reaped once killed.
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("what the command started outlived it")
		}
	}
}
