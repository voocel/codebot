package cron

// ProcessAlive reports whether a process with the given PID exists.
// Exported for reuse outside the scheduler (e.g. dream's consolidation lock).
func ProcessAlive(pid int) bool {
	return processAlive(pid)
}
