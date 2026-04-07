package agent

import (
	"strings"
	"time"

	"github.com/voocel/codebot/internal/apperr"
)

type ErrorSnapshot struct {
	Kind      apperr.Kind
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
	ErrorByKind           map[apperr.Kind]int
}

type runtimeMetrics struct {
	reminderTotal         int
	reminderByKind        map[RuntimeReminderKind]int
	compactionTotal       int
	compactionChanged     int
	compactionSaved       int
	compactionByKind      map[CompactionKind]int
	compactionSavedByKind map[CompactionKind]int
	errorTotal            int
	errorByKind           map[apperr.Kind]int
}

func newRuntimeMetrics() *runtimeMetrics {
	return &runtimeMetrics{
		reminderByKind:        make(map[RuntimeReminderKind]int),
		compactionByKind:      make(map[CompactionKind]int),
		compactionSavedByKind: make(map[CompactionKind]int),
		errorByKind:           make(map[apperr.Kind]int),
	}
}

func (s *Session) RuntimeMetrics() RuntimeMetricsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	reminderByKind := make(map[RuntimeReminderKind]int, len(s.metrics.reminderByKind))
	for k, v := range s.metrics.reminderByKind {
		reminderByKind[k] = v
	}
	compactionByKind := make(map[CompactionKind]int, len(s.metrics.compactionByKind))
	for k, v := range s.metrics.compactionByKind {
		compactionByKind[k] = v
	}
	compactionSavedByKind := make(map[CompactionKind]int, len(s.metrics.compactionSavedByKind))
	for k, v := range s.metrics.compactionSavedByKind {
		compactionSavedByKind[k] = v
	}
	errorByKind := make(map[apperr.Kind]int, len(s.metrics.errorByKind))
	for k, v := range s.metrics.errorByKind {
		errorByKind[k] = v
	}
	return RuntimeMetricsSnapshot{
		ReminderTotal:         s.metrics.reminderTotal,
		ReminderByKind:        reminderByKind,
		CompactionTotal:       s.metrics.compactionTotal,
		CompactionChanged:     s.metrics.compactionChanged,
		CompactionSaved:       s.metrics.compactionSaved,
		CompactionByKind:      compactionByKind,
		CompactionSavedByKind: compactionSavedByKind,
		ErrorTotal:            s.metrics.errorTotal,
		ErrorByKind:           errorByKind,
	}
}

func (s *Session) recordReminderMetric(kind RuntimeReminderKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metrics == nil {
		s.metrics = newRuntimeMetrics()
	}
	s.metrics.reminderTotal++
	s.metrics.reminderByKind[kind]++
}

func (s *Session) recordReminderSnapshot(kind RuntimeReminderKind, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReminder = &ReminderSnapshot{
		Kind:      kind,
		Mode:      mode,
		Timestamp: time.Now(),
	}
}

func (s *Session) recordCompactionAttempt(kind CompactionKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metrics == nil {
		s.metrics = newRuntimeMetrics()
	}
	s.metrics.compactionTotal++
	s.metrics.compactionByKind[kind]++
}

func (s *Session) recordCompactionResult(kind CompactionKind, changed bool, before, after int) {
	if !changed {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metrics == nil {
		s.metrics = newRuntimeMetrics()
	}
	s.metrics.compactionChanged++
	if before > after {
		s.metrics.compactionSaved += before - after
		s.metrics.compactionSavedByKind[kind] += before - after
	}
}

func (s *Session) recordCompactionSnapshot(kind CompactionKind, strategy, reason string, changed bool, before, after, compactedCount, keptCount int, splitTurn bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCompaction = &CompactionSnapshot{
		Kind:           kind,
		Strategy:       strategy,
		Reason:         reason,
		Changed:        changed,
		TokensBefore:   before,
		TokensAfter:    after,
		CompactedCount: compactedCount,
		KeptCount:      keptCount,
		SplitTurn:      splitTurn,
		Timestamp:      time.Now(),
	}
}

func (s *Session) recordErrorDiagnostic(err error) {
	if err == nil {
		return
	}

	snapshot := buildErrorSnapshot(err)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.metrics == nil {
		s.metrics = newRuntimeMetrics()
	}
	s.metrics.errorTotal++
	s.metrics.errorByKind[snapshot.Kind]++

	s.recentErrors = append(s.recentErrors, snapshot)
	if len(s.recentErrors) > maxRecentErrors {
		s.recentErrors = append([]ErrorSnapshot(nil), s.recentErrors[len(s.recentErrors)-maxRecentErrors:]...)
	}
}

func buildErrorSnapshot(err error) ErrorSnapshot {
	message := strings.TrimSpace(apperr.Format(err, ""))
	if message == "" {
		message = "error"
	}
	detail := strings.TrimSpace(err.Error())
	if detail == message {
		detail = ""
	} else if strings.HasPrefix(detail, message+": ") {
		detail = strings.TrimSpace(strings.TrimPrefix(detail, message+": "))
	}
	return ErrorSnapshot{
		Kind:      apperr.KindOf(err),
		Message:   message,
		Detail:    detail,
		Timestamp: time.Now(),
	}
}

func (s *Session) RecentErrors(limit int) []ErrorSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > len(s.recentErrors) {
		limit = len(s.recentErrors)
	}
	start := len(s.recentErrors) - limit
	snapshots := make([]ErrorSnapshot, limit)
	copy(snapshots, s.recentErrors[start:])
	return snapshots
}
