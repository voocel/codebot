// Package dream implements background memory consolidation: when the session
// goes idle, a restricted subagent reorganizes the auto-memory directory —
// merging duplicates, fixing stale facts, pruning the MEMORY.md index.
package dream

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/cron"
)

const (
	// lockFileName lives inside the memory directory. Its mtime IS the
	// lastConsolidatedAt timestamp; its body is the holder's PID.
	lockFileName = ".consolidate-lock"

	// holderStaleAfter bounds how long a live PID can hold the lock —
	// past this the PID is assumed reused and the lock is reclaimed.
	holderStaleAfter = time.Hour
)

// Lock is a file lock whose mtime doubles as the last-consolidation
// timestamp: acquiring writes the holder PID (pushing mtime to now), a
// successful run simply keeps that mtime, and a failed run rewinds it so
// the time gate reopens.
type Lock struct {
	dir string // memory directory
}

func NewLock(memoryDir string) *Lock {
	return &Lock{dir: memoryDir}
}

func (l *Lock) path() string {
	return filepath.Join(l.dir, lockFileName)
}

// LastConsolidatedAt returns the lock file's mtime, or the zero time when
// no consolidation has ever run. Cost: one stat.
func (l *Lock) LastConsolidatedAt() time.Time {
	info, err := os.Stat(l.path())
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// TryAcquire attempts to take the lock. On success it returns the
// pre-acquire mtime (for Rollback) and true; the file now holds this
// process's PID with mtime = now, which is all a successful run needs —
// no separate completion stamp.
//
// It yields (returns false) when another live process acquired within the
// last hour. A dead PID, an unparseable body, or an mtime older than an
// hour (PID-reuse guard) is reclaimed. After writing, the body is re-read
// to break ties between two concurrent reclaimers: the loser bails.
func (l *Lock) TryAcquire() (prior time.Time, ok bool) {
	path := l.path()

	var mtime time.Time
	holderPid := -1
	if info, err := os.Stat(path); err == nil {
		mtime = info.ModTime()
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				holderPid = pid
			}
		}
	}

	if !mtime.IsZero() && time.Since(mtime) < holderStaleAfter {
		if holderPid > 0 && cron.ProcessAlive(holderPid) {
			return time.Time{}, false
		}
	}

	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return time.Time{}, false
	}
	self := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(self), 0o644); err != nil {
		return time.Time{}, false
	}
	verify, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(verify)) != self {
		return time.Time{}, false
	}
	return mtime, true
}

// Complete marks a successful run: the PID body is cleared so this process
// no longer counts as a holder (a manual /dream may re-run immediately),
// and the fresh mtime becomes lastConsolidatedAt for the auto time gate.
func (l *Lock) Complete() {
	_ = os.WriteFile(l.path(), nil, 0o644)
}

// Rollback rewinds the lock to its pre-acquire state after a failed or
// killed run, reopening the time gate. A zero prior restores "never
// consolidated" (file removed). The PID body is cleared — this process is
// still alive and would otherwise look like a holder. Errors are swallowed:
// the worst case is the next trigger waiting a full MinHours again.
func (l *Lock) Rollback(prior time.Time) {
	path := l.path()
	if prior.IsZero() {
		_ = os.Remove(path)
		return
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return
	}
	_ = os.Chtimes(path, prior, prior)
}
