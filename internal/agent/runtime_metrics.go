package agent

import "time"

type RuntimeMetricsSnapshot struct {
	ReminderTotal         int
	ReminderByKind        map[RuntimeReminderKind]int
	CompactionTotal       int
	CompactionChanged     int
	CompactionSaved       int
	CompactionByKind      map[CompactionKind]int
	CompactionSavedByKind map[CompactionKind]int
}

type runtimeMetrics struct {
	reminderTotal         int
	reminderByKind        map[RuntimeReminderKind]int
	compactionTotal       int
	compactionChanged     int
	compactionSaved       int
	compactionByKind      map[CompactionKind]int
	compactionSavedByKind map[CompactionKind]int
}

func newRuntimeMetrics() *runtimeMetrics {
	return &runtimeMetrics{
		reminderByKind:        make(map[RuntimeReminderKind]int),
		compactionByKind:      make(map[CompactionKind]int),
		compactionSavedByKind: make(map[CompactionKind]int),
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
	return RuntimeMetricsSnapshot{
		ReminderTotal:         s.metrics.reminderTotal,
		ReminderByKind:        reminderByKind,
		CompactionTotal:       s.metrics.compactionTotal,
		CompactionChanged:     s.metrics.compactionChanged,
		CompactionSaved:       s.metrics.compactionSaved,
		CompactionByKind:      compactionByKind,
		CompactionSavedByKind: compactionSavedByKind,
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
