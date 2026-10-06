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

// Cancelled, a command gives up at once, and what it started goes with it,
// though it holds the command's output.
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
	// Orphaned, it is reaped once killed.
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("what the command started outlived it")
		}
	}
}
