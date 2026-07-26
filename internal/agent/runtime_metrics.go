package agent

import (
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/voocel/codebot/internal/diag"
)

type ErrorSnapshot struct {
	Category  diag.Category
	Message   string
	Detail    string
	Timestamp time.Time
}

type RuntimeMetricsSnapshot struct {
	ReminderTotal         int
	ReminderByKind        map[RuntimeReminderKind]int
	CompactionTotal       int
	CompactionChanged     int
	CompactionSaved       int
	CompactionByKind      map[CompactionKind]int
	CompactionSavedByKind map[CompactionKind]int
	ErrorTotal            int
	ErrorByCategory       map[diag.Category]int
}

// runtimeMetrics owns the session's diagnostic counters plus the error ring
// and last-compaction snapshot, guarded by its own lock. recordError sits on
// the emit path (every SEError / agent EventError), so keeping it off s.mu is
// what lets emit run without touching the session lock at all.
// The zero value is usable; maps are allocated on first write.
type runtimeMetrics struct {
	mu sync.Mutex

	reminderTotal         int
	reminderByKind        map[RuntimeReminderKind]int
	compactionTotal       int
	compactionChanged     int
	compactionSaved       int
	compactionByKind      map[CompactionKind]int
	compactionSavedByKind map[CompactionKind]int
	errorTotal            int
	errorByCategory       map[diag.Category]int

	recentErrors   []ErrorSnapshot
	lastCompaction *CompactionSnapshot
}

func (m *runtimeMetrics) recordReminder(kind RuntimeReminderKind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reminderByKind == nil {
		m.reminderByKind = make(map[RuntimeReminderKind]int)
	}
	m.reminderTotal++
	m.reminderByKind[kind]++
}

func (m *runtimeMetrics) recordCompactionAttempt(kind CompactionKind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.compactionByKind == nil {
		m.compactionByKind = make(map[CompactionKind]int)
	}
	m.compactionTotal++
	m.compactionByKind[kind]++
}

func (m *runtimeMetrics) recordCompactionResult(kind CompactionKind, changed bool, before, after int) {
	if !changed {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compactionChanged++
	if before > after {
		if m.compactionSavedByKind == nil {
			m.compactionSavedByKind = make(map[CompactionKind]int)
		}
		m.compactionSaved += before - after
		m.compactionSavedByKind[kind] += before - after
	}
}

// recordCompactionSnapshot stamps the timestamp itself so callers pass only
// the compaction facts.
func (m *runtimeMetrics) recordCompactionSnapshot(snap CompactionSnapshot) {
	snap.Timestamp = time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCompaction = &snap
}

func (m *runtimeMetrics) recordError(err error) {
	if err == nil {
		return
	}
	snapshot := buildErrorSnapshot(err)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errorByCategory == nil {
		m.errorByCategory = make(map[diag.Category]int)
	}
	m.errorTotal++
	m.errorByCategory[snapshot.Category]++

	m.recentErrors = append(m.recentErrors, snapshot)
	if len(m.recentErrors) > maxRecentErrors {
		m.recentErrors = append([]ErrorSnapshot(nil), m.recentErrors[len(m.recentErrors)-maxRecentErrors:]...)
	}
}

func (m *runtimeMetrics) snapshot() RuntimeMetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap := RuntimeMetricsSnapshot{
		ReminderTotal:         m.reminderTotal,
		ReminderByKind:        make(map[RuntimeReminderKind]int, len(m.reminderByKind)),
		CompactionTotal:       m.compactionTotal,
		CompactionChanged:     m.compactionChanged,
		CompactionSaved:       m.compactionSaved,
		CompactionByKind:      make(map[CompactionKind]int, len(m.compactionByKind)),
		CompactionSavedByKind: make(map[CompactionKind]int, len(m.compactionSavedByKind)),
		ErrorTotal:            m.errorTotal,
		ErrorByCategory:       make(map[diag.Category]int, len(m.errorByCategory)),
	}
	maps.Copy(snap.ReminderByKind, m.reminderByKind)
	maps.Copy(snap.CompactionByKind, m.compactionByKind)
	maps.Copy(snap.CompactionSavedByKind, m.compactionSavedByKind)
	maps.Copy(snap.ErrorByCategory, m.errorByCategory)
	return snap
}

func (m *runtimeMetrics) recent(limit int) []ErrorSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limit <= 0 || limit > len(m.recentErrors) {
		limit = len(m.recentErrors)
	}
	start := len(m.recentErrors) - limit
	snapshots := make([]ErrorSnapshot, limit)
	copy(snapshots, m.recentErrors[start:])
	return snapshots
}

func (m *runtimeMetrics) lastCompactionSnapshot() (CompactionSnapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastCompaction == nil {
		return CompactionSnapshot{}, false
	}
	return *m.lastCompaction, true
}

func (m *runtimeMetrics) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reminderTotal = 0
	m.reminderByKind = nil
	m.compactionTotal = 0
	m.compactionChanged = 0
	m.compactionSaved = 0
	m.compactionByKind = nil
	m.compactionSavedByKind = nil
	m.errorTotal = 0
	m.errorByCategory = nil
	m.recentErrors = nil
	m.lastCompaction = nil
}

func (s *Session) RuntimeMetrics() RuntimeMetricsSnapshot {
	return s.metrics.snapshot()
}

func (s *Session) RecentErrors(limit int) []ErrorSnapshot {
	return s.metrics.recent(limit)
}

func buildErrorSnapshot(err error) ErrorSnapshot {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "error"
	}
	return ErrorSnapshot{
		Category:  diag.Categorize(err),
		Message:   message,
		Timestamp: time.Now(),
	}
}
