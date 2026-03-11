package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	MaxJobs         = 50
	RecurringMaxAge = 3 * 24 * time.Hour

	scheduledTasksFile = "scheduled_tasks.json"
	scheduledTasksLock = "scheduled_tasks.lock"
)

// Job is a scheduled task.
type Job struct {
	ID        string `json:"id"`
	Schedule  string `json:"schedule"`
	Prompt    string `json:"prompt"`
	Recurring bool   `json:"recurring,omitempty"`
	CreatedAt int64  `json:"createdAt"` // Unix milliseconds (matches Claude Code format)
	Durable   bool   `json:"durable,omitempty"`

	interval time.Duration // parsed interval (mutually exclusive with cronExpr)
	cronExpr *CronExpr    // parsed cron expression
	nextFire time.Time
}

// NextFire returns the next scheduled fire time.
func (j *Job) NextFire() time.Time { return j.nextFire }

// CreatedTime returns CreatedAt as time.Time.
func (j *Job) CreatedTime() time.Time { return time.UnixMilli(j.CreatedAt) }

// CronExpr holds parsed 5-field cron expression values.
type CronExpr struct {
	Minute     []int
	Hour       []int
	DayOfMonth []int
	Month      []int
	DayOfWeek  []int
}

// ---------------------------------------------------------------------------
// Store — dual-track: session (in-memory) + durable (file-backed)
// ---------------------------------------------------------------------------

// Store manages both session and durable cron jobs.
type Store struct {
	mu          sync.RWMutex
	sessionJobs map[string]*Job // in-memory only
	durableJobs map[string]*Job // loaded from file
	configDir   string          // e.g. <cwd>/.codebot — empty disables durable

	writeCh chan struct{} // capacity 1 — coalesces multiple write requests
	closeCh chan struct{} // signals writer goroutine to stop
	writeMu sync.Mutex   // serializes StartWriter/StopWriter calls
	writing bool         // true while writer goroutine is running
}

// NewStore creates an empty Store. Call SetConfigDir to enable durable storage.
func NewStore() *Store {
	return &Store{
		sessionJobs: make(map[string]*Job),
		durableJobs: make(map[string]*Job),
	}
}

// SetConfigDir enables durable storage (e.g. cwd/.codebot).
func (s *Store) SetConfigDir(dir string) {
	s.mu.Lock()
	s.configDir = dir
	s.mu.Unlock()
}

// ConfigDir returns the configured directory for persistence.
func (s *Store) ConfigDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configDir
}

// Create adds a new job. If durable=true it is also persisted to file.
func (s *Store) Create(schedule, prompt string, recurring, durable bool) (*Job, error) {
	cronExpr, interval, err := ParseSchedule(schedule)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	total := len(s.sessionJobs) + len(s.durableJobs)
	if total >= MaxJobs {
		return nil, fmt.Errorf("too many scheduled jobs (max %d). Cancel one first", MaxJobs)
	}

	id := genID()
	now := time.Now()

	var nextFire time.Time
	switch {
	case interval > 0:
		nextFire = now.Add(interval)
	case recurring:
		nf, ok := JitteredNextFire(cronExpr, now, id)
		if !ok {
			return nil, fmt.Errorf("cron expression does not match any date in the next year")
		}
		nextFire = nf
	default:
		nf, ok := OneShotJitteredFireTime(cronExpr, now, id)
		if !ok {
			return nil, fmt.Errorf("cron expression does not match any date in the next year")
		}
		nextFire = nf
	}

	j := &Job{
		ID:        id,
		Schedule:  schedule,
		Prompt:    prompt,
		Recurring: recurring,
		CreatedAt: now.UnixMilli(),
		Durable:   durable,
		interval:  interval,
		cronExpr:  cronExpr,
		nextFire:  nextFire,
	}

	if durable && s.configDir != "" {
		s.durableJobs[id] = j
		s.scheduleDurableWrite()
	} else {
		j.Durable = false
		s.sessionJobs[id] = j
	}

	cp := *j
	return &cp, nil
}

// Delete removes a job from either track.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessionJobs[id]; ok {
		delete(s.sessionJobs, id)
		return nil
	}
	if _, ok := s.durableJobs[id]; ok {
		delete(s.durableJobs, id)
		s.scheduleDurableWrite()
		return nil
	}
	return fmt.Errorf("job %s not found", id)
}

// DeleteAll removes all jobs from both tracks and returns the count.
func (s *Store) DeleteAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.sessionJobs) + len(s.durableJobs)
	clear(s.sessionJobs)
	if len(s.durableJobs) > 0 {
		clear(s.durableJobs)
		s.scheduleDurableWrite()
	}
	return n
}

// List returns a snapshot of all jobs (session + durable) sorted by creation time.
func (s *Store) List() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]Job, 0, len(s.sessionJobs)+len(s.durableJobs))
	for _, j := range s.sessionJobs {
		jobs = append(jobs, *j)
	}
	for _, j := range s.durableJobs {
		jobs = append(jobs, *j)
	}
	sort.Slice(jobs, func(i, k int) bool {
		return jobs[i].CreatedAt < jobs[k].CreatedAt
	})
	return jobs
}

// Get returns a copy of the job or false.
func (s *Store) Get(id string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if j, ok := s.sessionJobs[id]; ok {
		return *j, true
	}
	if j, ok := s.durableJobs[id]; ok {
		return *j, true
	}
	return Job{}, false
}

// Len returns total count of all jobs.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessionJobs) + len(s.durableJobs)
}

// ---------------------------------------------------------------------------
// Tick — used by scheduler
// ---------------------------------------------------------------------------

// TickSessionJobs fires ready session jobs. Returns prompts and removes expired/one-shot.
func (s *Store) TickSessionJobs(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return tickMap(s.sessionJobs, now)
}

// TickDurableJobs fires ready durable jobs. Returns prompts and persists changes.
func (s *Store) TickDurableJobs(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	prompts := tickMap(s.durableJobs, now)
	if len(prompts) > 0 {
		s.scheduleDurableWrite()
	}
	return prompts
}

func tickMap(jobs map[string]*Job, now time.Time) []string {
	var prompts []string
	var toDelete []string

	for id, j := range jobs {
		if now.Before(j.nextFire) {
			continue
		}
		prompts = append(prompts, j.Prompt)

		if !j.Recurring {
			toDelete = append(toDelete, id)
			continue
		}

		// 3-day expiry for recurring (non-permanent) jobs.
		if now.Sub(j.CreatedTime()) > RecurringMaxAge {
			toDelete = append(toDelete, id)
			continue
		}

		// Advance nextFire.
		if j.interval > 0 {
			j.nextFire = now.Add(j.interval)
		} else if j.cronExpr != nil {
			nf, ok := JitteredNextFire(j.cronExpr, now, j.ID)
			if !ok {
				toDelete = append(toDelete, id)
				continue
			}
			j.nextFire = nf
		}
	}
	for _, id := range toDelete {
		delete(jobs, id)
	}
	return prompts
}

// ---------------------------------------------------------------------------
// Async durable writer
// ---------------------------------------------------------------------------

// StartWriter launches the background goroutine that writes durable jobs to disk.
func (s *Store) StartWriter() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writing {
		return
	}
	s.writeCh = make(chan struct{}, 1)
	s.closeCh = make(chan struct{})
	s.writing = true
	go s.writerLoop()
}

// StopWriter stops the writer goroutine and waits for the final flush.
func (s *Store) StopWriter() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.writing {
		return
	}
	close(s.closeCh)
	// Drain remaining write to ensure final flush.
	s.flushDurable()
	s.writing = false
}

func (s *Store) writerLoop() {
	for {
		select {
		case <-s.writeCh:
			s.flushDurable()
		case <-s.closeCh:
			return
		}
	}
}

// scheduleDurableWrite signals the writer goroutine to persist durable jobs.
// Non-blocking: if a write is already pending, the signal is coalesced.
func (s *Store) scheduleDurableWrite() {
	if s.writeCh == nil {
		// Writer not started — fall back to synchronous write.
		s.writeDurableSync()
		return
	}
	select {
	case s.writeCh <- struct{}{}:
	default: // already pending — coalesced
	}
}

// flushDurable serializes under RLock and writes outside the lock.
func (s *Store) flushDurable() {
	s.mu.RLock()
	data := s.marshalDurableJobs()
	dir := s.configDir
	s.mu.RUnlock()

	if dir == "" || data == nil {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, scheduledTasksFile), data, 0o644)
}

// marshalDurableJobs serializes durable jobs to JSON. Must hold at least RLock.
func (s *Store) marshalDurableJobs() []byte {
	fd := durableFileData{Tasks: make([]durableTask, 0, len(s.durableJobs))}
	for _, j := range s.durableJobs {
		fd.Tasks = append(fd.Tasks, durableTask{
			ID:        j.ID,
			Cron:      j.Schedule,
			Prompt:    j.Prompt,
			CreatedAt: j.CreatedAt,
			Recurring: j.Recurring,
		})
	}
	data, err := json.MarshalIndent(fd, "", "  ")
	if err != nil {
		return nil
	}
	return append(data, '\n')
}

// writeDurableSync is the fallback synchronous write (used when writer is not started).
func (s *Store) writeDurableSync() {
	if s.configDir == "" {
		return
	}
	_ = os.MkdirAll(s.configDir, 0o755)
	data := s.marshalDurableJobs()
	if data != nil {
		_ = os.WriteFile(s.durableFilePath(), data, 0o644)
	}
}

// ---------------------------------------------------------------------------
// Durable file I/O — .codebot/scheduled_tasks.json
// ---------------------------------------------------------------------------

// durableFileData is the JSON structure persisted to disk.
type durableFileData struct {
	Tasks []durableTask `json:"tasks"`
}

type durableTask struct {
	ID        string `json:"id"`
	Cron      string `json:"cron"`
	Prompt    string `json:"prompt"`
	CreatedAt int64  `json:"createdAt"`
	Recurring bool   `json:"recurring,omitempty"`
}

func (s *Store) durableFilePath() string {
	return filepath.Join(s.configDir, scheduledTasksFile)
}

// LockFilePath returns the lock file path for the scheduler.
func (s *Store) LockFilePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return filepath.Join(s.configDir, scheduledTasksLock)
}

// DurableFilePath returns the tasks file path.
func (s *Store) DurableFilePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.durableFilePath()
}

// LoadDurableJobs reads .codebot/scheduled_tasks.json and loads valid jobs.
func (s *Store) LoadDurableJobs() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.configDir == "" {
		return nil
	}

	data, err := os.ReadFile(s.durableFilePath())
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return err
	}

	var fd durableFileData
	if err := json.Unmarshal(data, &fd); err != nil {
		return nil // malformed file → treat as empty
	}

	clear(s.durableJobs)
	now := time.Now()
	for _, dt := range fd.Tasks {
		if dt.ID == "" || dt.Cron == "" || dt.Prompt == "" || dt.CreatedAt == 0 {
			continue
		}
		cronExpr, interval, err := ParseSchedule(dt.Cron)
		if err != nil {
			continue
		}

		var nextFire time.Time
		switch {
		case interval > 0:
			nextFire = now.Add(interval)
		case dt.Recurring:
			nf, ok := JitteredNextFire(cronExpr, now, dt.ID)
			if !ok {
				continue
			}
			nextFire = nf
		default:
			nf, ok := OneShotJitteredFireTime(cronExpr, time.UnixMilli(dt.CreatedAt), dt.ID)
			if !ok {
				continue
			}
			nextFire = nf
		}

		s.durableJobs[dt.ID] = &Job{
			ID:        dt.ID,
			Schedule:  dt.Cron,
			Prompt:    dt.Prompt,
			Recurring: dt.Recurring,
			CreatedAt: dt.CreatedAt,
			Durable:   true,
			interval:  interval,
			cronExpr:  cronExpr,
			nextFire:  nextFire,
		}
	}
	return nil
}

// DeleteDurableByIDs removes durable jobs by ID and persists. Used by scheduler for cleanup.
func (s *Store) DeleteDurableByIDs(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.durableJobs, id)
	}
	s.scheduleDurableWrite()
}

// MissedDurableJobs returns durable one-shot jobs whose first fire time is in the past.
func (s *Store) MissedDurableJobs() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	var missed []Job
	for _, j := range s.durableJobs {
		if j.Recurring {
			continue
		}
		if j.cronExpr == nil {
			continue
		}
		nf, ok := NextMatch(j.cronExpr, j.CreatedTime())
		if !ok {
			continue
		}
		if nf.Before(now) {
			missed = append(missed, *j)
		}
	}
	return missed
}

// ---------------------------------------------------------------------------
// File Lock — .codebot/scheduled_tasks.lock
// ---------------------------------------------------------------------------

// lockData is the JSON written to the lock file.
type lockData struct {
	SessionID  string `json:"sessionId"`
	PID        int    `json:"pid"`
	AcquiredAt int64  `json:"acquiredAt"`
}

// AcquireLock tries to acquire the scheduler lock. Returns true if acquired.
func (s *Store) AcquireLock(sessionID string) bool {
	s.mu.RLock()
	lockPath := filepath.Join(s.configDir, scheduledTasksLock)
	s.mu.RUnlock()

	if lockPath == scheduledTasksLock { // no configDir
		return false
	}

	ld := lockData{
		SessionID:  sessionID,
		PID:        os.Getpid(),
		AcquiredAt: time.Now().UnixMilli(),
	}

	// Try atomic create (O_EXCL).
	data, _ := json.Marshal(ld)
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755)
	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		_, _ = f.Write(data)
		f.Close()
		return true
	}

	// Lock exists — read and check.
	existing := s.readLock(lockPath)

	// We already own it.
	if existing != nil && existing.SessionID == sessionID {
		_ = os.WriteFile(lockPath, data, 0o644)
		return true
	}

	// Another session owns it and its process is alive.
	if existing != nil && processAlive(existing.PID) {
		return false
	}

	// Stale lock — recover.
	_ = os.Remove(lockPath)
	f, err = os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		_, _ = f.Write(data)
		f.Close()
		return true
	}
	return false
}

// ReleaseLock releases the scheduler lock if we own it.
func (s *Store) ReleaseLock(sessionID string) {
	s.mu.RLock()
	lockPath := filepath.Join(s.configDir, scheduledTasksLock)
	s.mu.RUnlock()

	existing := s.readLock(lockPath)
	if existing == nil || existing.SessionID != sessionID {
		return
	}
	_ = os.Remove(lockPath)
}

func (s *Store) readLock(lockPath string) *lockData {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil
	}
	var ld lockData
	if err := json.Unmarshal(data, &ld); err != nil {
		return nil
	}
	if ld.SessionID == "" || ld.PID == 0 || ld.AcquiredAt == 0 {
		return nil
	}
	return &ld
}

// processAlive checks if a PID is alive by sending signal 0.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// ---------------------------------------------------------------------------
// Jitter — deterministic per-ID offset to avoid thundering herd
// ---------------------------------------------------------------------------

const (
	jitterRecurringFrac = 0.1 // up to 10% of interval
	jitterRecurringCap  = 15 * time.Minute

	oneShotMaxJitter = 90 * time.Second // one-shots on :00/:30 fire up to 90s early
	oneShotMinuteMod = 30               // only jitter when minute % 30 == 0
)

// idToFraction returns a deterministic [0, 1) fraction from a hex ID.
func idToFraction(id string) float64 {
	s := id
	if len(s) > 8 {
		s = s[:8]
	}
	n, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0
	}
	f := float64(n) / 4294967296.0
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0
	}
	return f
}

// OneShotJitteredFireTime computes the fire time for a one-shot cron job.
// If the fire minute lands on :00 or :30, it shifts up to 90s early to avoid thundering herd.
func OneShotJitteredFireTime(expr *CronExpr, createdAt time.Time, id string) (time.Time, bool) {
	nf, ok := NextMatch(expr, createdAt)
	if !ok {
		return time.Time{}, false
	}
	if nf.Minute()%oneShotMinuteMod != 0 {
		return nf, true
	}
	earlyShift := time.Duration(idToFraction(id) * float64(oneShotMaxJitter))
	result := nf.Add(-earlyShift)
	if result.Before(createdAt) {
		result = createdAt
	}
	return result, true
}

// JitteredNextFire computes the next fire time with jitter for a recurring cron job.
func JitteredNextFire(expr *CronExpr, after time.Time, id string) (time.Time, bool) {
	nf, ok := NextMatch(expr, after)
	if !ok {
		return time.Time{}, false
	}
	// Compute interval by finding the next-next match.
	nnf, ok := NextMatch(expr, nf)
	if !ok {
		return nf, true
	}
	interval := nnf.Sub(nf)
	jitter := time.Duration(math.Min(
		idToFraction(id)*jitterRecurringFrac*float64(interval),
		float64(jitterRecurringCap),
	))
	return nf.Add(jitter), true
}

// ---------------------------------------------------------------------------
// Schedule parsing
// ---------------------------------------------------------------------------

// ParseSchedule tries to parse s as a Go duration first, then as a 5-field cron.
func ParseSchedule(s string) (cronExpr *CronExpr, interval time.Duration, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, 0, fmt.Errorf("empty schedule")
	}

	// Try duration: "5m", "1h30m", "10s", etc.
	if d, dErr := time.ParseDuration(s); dErr == nil {
		if d < time.Second {
			return nil, 0, fmt.Errorf("interval too short (minimum 1s)")
		}
		return nil, d, nil
	}

	// Try cron expression.
	expr, cErr := ParseCron(s)
	if cErr != nil {
		return nil, 0, fmt.Errorf("invalid schedule %q: not a duration or cron expression", s)
	}
	return expr, 0, nil
}

// fieldRange defines the valid range for a cron field.
type fieldRange struct {
	min, max int
}

var fieldRanges = []fieldRange{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 6},  // day of week (0=Sun)
}

// ParseCron parses a 5-field cron expression.
func ParseCron(expr string) (*CronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	parsed := make([][]int, 5)
	for i, f := range fields {
		vals, err := parseCronField(f, fieldRanges[i])
		if err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i+1, f, err)
		}
		parsed[i] = vals
	}

	return &CronExpr{
		Minute:     parsed[0],
		Hour:       parsed[1],
		DayOfMonth: parsed[2],
		Month:      parsed[3],
		DayOfWeek:  parsed[4],
	}, nil
}

// parseCronField parses a single cron field (supports *, */N, N, N-M, N-M/S, N,M).
func parseCronField(field string, r fieldRange) ([]int, error) {
	set := make(map[int]struct{})

	for part := range strings.SplitSeq(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty part")
		}

		// */N or *
		if strings.HasPrefix(part, "*") {
			step := 1
			if rest := strings.TrimPrefix(part, "*"); rest != "" {
				if !strings.HasPrefix(rest, "/") {
					return nil, fmt.Errorf("invalid syntax %q", part)
				}
				s, err := strconv.Atoi(rest[1:])
				if err != nil || s < 1 {
					return nil, fmt.Errorf("invalid step in %q", part)
				}
				step = s
			}
			for i := r.min; i <= r.max; i += step {
				set[i] = struct{}{}
			}
			continue
		}

		// N-M/S or N-M
		if strings.Contains(part, "-") {
			rangePart, stepPart, _ := strings.Cut(part, "/")
			bounds := strings.SplitN(rangePart, "-", 2)
			lo, err := strconv.Atoi(bounds[0])
			if err != nil {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			hi, err := strconv.Atoi(bounds[1])
			if err != nil {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			step := 1
			if stepPart != "" {
				step, err = strconv.Atoi(stepPart)
				if err != nil || step < 1 {
					return nil, fmt.Errorf("invalid step in %q", part)
				}
			}
			isDOW := r.min == 0 && r.max == 6
			upper := r.max
			if isDOW {
				upper = 7
			}
			if lo < r.min || hi > upper || lo > hi {
				return nil, fmt.Errorf("range out of bounds in %q", part)
			}
			for i := lo; i <= hi; i += step {
				v := i
				if isDOW && v == 7 {
					v = 0
				}
				set[v] = struct{}{}
			}
			continue
		}

		// Single number.
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q", part)
		}
		isDOW := r.min == 0 && r.max == 6
		if isDOW && v == 7 {
			v = 0
		}
		if v < r.min || v > r.max {
			return nil, fmt.Errorf("value %d out of range [%d, %d]", v, r.min, r.max)
		}
		set[v] = struct{}{}
	}

	if len(set) == 0 {
		return nil, fmt.Errorf("no values")
	}

	vals := make([]int, 0, len(set))
	for v := range set {
		vals = append(vals, v)
	}
	sort.Ints(vals)
	return vals, nil
}

// NextMatch finds the next time after 'after' that matches the cron expression.
// Implements standard cron DOM/DOW union logic: when both are restricted,
// a day matches if EITHER condition is true (OR semantics).
func NextMatch(expr *CronExpr, after time.Time) (time.Time, bool) {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.Add(366 * 24 * time.Hour)

	allDOM := len(expr.DayOfMonth) == 31
	allDOW := len(expr.DayOfWeek) == 7

	for t.Before(limit) {
		if !contains(expr.Month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}

		domMatch := contains(expr.DayOfMonth, t.Day())
		dowMatch := contains(expr.DayOfWeek, int(t.Weekday()))
		var dayMatch bool
		switch {
		case allDOM && allDOW:
			dayMatch = true
		case allDOM:
			dayMatch = dowMatch
		case allDOW:
			dayMatch = domMatch
		default:
			dayMatch = domMatch || dowMatch
		}

		if !dayMatch {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !contains(expr.Hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !contains(expr.Minute, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

func contains(set []int, v int) bool {
	return slices.Contains(set, v)
}

// ---------------------------------------------------------------------------
// HumanSchedule
// ---------------------------------------------------------------------------

// HumanSchedule returns a human-readable description of a schedule string.
func HumanSchedule(schedule string) string {
	cronExpr, interval, err := ParseSchedule(schedule)
	if err != nil {
		return schedule
	}
	if interval > 0 {
		return "Every " + humanDuration(interval)
	}
	return humanCron(cronExpr)
}

func humanDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0 && d >= time.Hour:
		h := int(d / time.Hour)
		if h == 1 {
			return "hour"
		}
		return fmt.Sprintf("%d hours", h)
	case d%time.Minute == 0 && d >= time.Minute:
		m := int(d / time.Minute)
		if m == 1 {
			return "minute"
		}
		return fmt.Sprintf("%d minutes", m)
	default:
		s := int(d / time.Second)
		if s == 1 {
			return "second"
		}
		return fmt.Sprintf("%d seconds", s)
	}
}

var dayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func humanCron(expr *CronExpr) string {
	allMinutes := len(expr.Minute) == 60
	allHours := len(expr.Hour) == 24
	allDOM := len(expr.DayOfMonth) == 31
	allMonths := len(expr.Month) == 12
	allDOW := len(expr.DayOfWeek) == 7

	if allMinutes && allHours && allDOM && allMonths && allDOW {
		return "Every minute"
	}

	// */N * * * * → "Every N minutes"
	if allHours && allDOM && allMonths && allDOW && !allMinutes {
		if len(expr.Minute) == 1 && expr.Minute[0] == 0 {
			return "Every hour"
		}
		if isStep(expr.Minute, 60) && len(expr.Minute) >= 2 {
			step := expr.Minute[1] - expr.Minute[0]
			if step == 1 {
				return "Every minute"
			}
			return fmt.Sprintf("Every %d minutes", step)
		}
		if len(expr.Minute) == 1 {
			return fmt.Sprintf("Every hour at :%02d", expr.Minute[0])
		}
	}

	// M */N * * * → "Every N hours at :MM"
	if len(expr.Minute) == 1 && !allHours && isStep(expr.Hour, 24) && allDOM && allMonths && allDOW {
		step := 1
		if len(expr.Hour) >= 2 {
			step = expr.Hour[1] - expr.Hour[0]
		}
		suffix := ""
		if expr.Minute[0] != 0 {
			suffix = fmt.Sprintf(" at :%02d", expr.Minute[0])
		}
		if step == 1 {
			return "Every hour" + suffix
		}
		return fmt.Sprintf("Every %d hours%s", step, suffix)
	}

	if len(expr.Minute) == 1 && len(expr.Hour) == 1 {
		timeStr := fmt.Sprintf("%02d:%02d", expr.Hour[0], expr.Minute[0])

		if allDOM && allMonths && allDOW {
			return "Every day at " + timeStr
		}
		if allDOM && allMonths && len(expr.DayOfWeek) == 5 &&
			expr.DayOfWeek[0] == 1 && expr.DayOfWeek[4] == 5 {
			return "Weekdays at " + timeStr
		}
		if allDOM && allMonths && len(expr.DayOfWeek) == 1 {
			return fmt.Sprintf("Every %s at %s", dayNames[expr.DayOfWeek[0]], timeStr)
		}
	}

	return "Cron: " + describeCronExpr(expr)
}

func describeCronExpr(expr *CronExpr) string {
	parts := make([]string, 5)
	parts[0] = describeField(expr.Minute, 0, 59)
	parts[1] = describeField(expr.Hour, 0, 23)
	parts[2] = describeField(expr.DayOfMonth, 1, 31)
	parts[3] = describeField(expr.Month, 1, 12)
	parts[4] = describeField(expr.DayOfWeek, 0, 6)
	return strings.Join(parts, " ")
}

func describeField(vals []int, min, max int) string {
	if len(vals) == max-min+1 {
		return "*"
	}
	strs := make([]string, len(vals))
	for i, v := range vals {
		strs[i] = strconv.Itoa(v)
	}
	return strings.Join(strs, ",")
}

func isStep(vals []int, max int) bool {
	if len(vals) < 2 {
		return false
	}
	step := vals[1] - vals[0]
	if step <= 0 {
		return false
	}
	expected := 0
	for _, v := range vals {
		if v != expected {
			return false
		}
		expected += step
	}
	return expected <= max+step
}

// ---------------------------------------------------------------------------
// ID generation
// ---------------------------------------------------------------------------

func genID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Missed task notification
// ---------------------------------------------------------------------------

// BuildMissedNotification creates a prompt text asking the user about missed tasks.
func BuildMissedNotification(tasks []Job) string {
	plural := len(tasks) > 1
	var sb strings.Builder
	if plural {
		sb.WriteString("The following one-shot scheduled tasks were missed while the agent was not running. ")
		sb.WriteString("They have already been removed from scheduled_tasks.json.\n")
		sb.WriteString("Do NOT execute these prompts yet. First use the ask_user tool to ask whether to run each one now. Only execute if the user confirms.\n\n")
	} else {
		sb.WriteString("The following one-shot scheduled task was missed while the agent was not running. ")
		sb.WriteString("It has already been removed from scheduled_tasks.json.\n")
		sb.WriteString("Do NOT execute this prompt yet. First use the ask_user tool to ask whether to run it now. Only execute if the user confirms.\n\n")
	}
	for _, t := range tasks {
		fmt.Fprintf(&sb, "[%s, created %s]\n```\n%s\n```\n\n",
			HumanSchedule(t.Schedule), t.CreatedTime().Format(time.DateTime), t.Prompt)
	}
	return sb.String()
}
