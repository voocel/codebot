package filelock

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHelperHolds is the other process: it holds the lock until its stdin
// closes.
func TestHelperHolds(t *testing.T) {
	path := os.Getenv("FILELOCK_HELPER")
	if path == "" {
		t.Skip("run by TestOtherProcessesWait")
	}
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	bufio.NewReader(os.Stdin).ReadString('\n')
	unlock()
}

// A lock another process holds is waited for.
func TestOtherProcessesWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHolds$")
	cmd.Env = append(os.Environ(), "FILELOCK_HELPER="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); line != "locked\n" {
		t.Fatalf("the helper said %q, %v", line, err)
	}

	got := make(chan func())
	go func() {
		unlock, err := Lock(path)
		if err != nil {
			t.Error(err)
		}
		got <- unlock
	}()
	select {
	case <-got:
		t.Fatal("took the lock another process holds")
	case <-time.After(200 * time.Millisecond):
	}
	stdin.Close()
	select {
	case unlock := <-got:
		unlock()
	case <-time.After(10 * time.Second):
		t.Fatal("the lock was not released with the process")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

// Within a process, each Lock excludes the others.
func TestGoroutinesTakeTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	var wg sync.WaitGroup
	// The race detector cannot see an flock order the goroutines.
	var held atomic.Int32
	for range 20 {
		wg.Go(func() {
			unlock, err := Lock(path)
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			if held.Add(1) != 1 {
				t.Error("two hold the lock")
			}
			time.Sleep(time.Millisecond)
			held.Add(-1)
		})
	}
	wg.Wait()
}
