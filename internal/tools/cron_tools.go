package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/cron"
)

// NewCronTools creates a cron.Store and the three cron tools that share it.
func NewCronTools() (*cron.Store, []agentcore.Tool) {
	store := cron.NewStore()
	return store, []agentcore.Tool{
		&CronCreateTool{store: store},
		&CronDeleteTool{store: store},
		&CronListTool{store: store},
	}
}

// ---------------------------------------------------------------------------
// CronCreateTool
// ---------------------------------------------------------------------------

// CronCreateTool creates a scheduled cron job.
type CronCreateTool struct{ store *cron.Store }

func (t *CronCreateTool) Name() string  { return "cron_create" }
func (t *CronCreateTool) Label() string { return "Create Cron Job" }
func (t *CronCreateTool) Description() string {
	return `Create a scheduled job that fires a prompt at the specified interval or cron schedule.
Schedule can be a Go duration (e.g. "5m", "1h") or a 5-field cron expression (e.g. "*/10 * * * *").
Jobs are session-only by default and destroyed when the session ends. Recurring jobs auto-expire after 3 days.`
}

// Schema exposes schedule, prompt, recurring to the LLM.
// The durable parameter is intentionally omitted from the LLM schema
// but accepted internally (used by /loop command).
func (t *CronCreateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("schedule", schema.String("Interval (e.g. '5m', '1h') or cron expression (e.g. '*/5 * * * *')")).Required(),
		schema.Property("prompt", schema.String("The prompt text to send when the job fires")).Required(),
		schema.Property("recurring", schema.Bool("Whether the job repeats (default: true)")),
	)
}

// Store returns the underlying cron.Store.
func (t *CronCreateTool) Store() *cron.Store { return t.store }

type cronCreateArgs struct {
	Schedule  string `json:"schedule"`
	Prompt    string `json:"prompt"`
	Recurring *bool  `json:"recurring,omitempty"`
	Durable   *bool  `json:"durable,omitempty"` // hidden from LLM, default false
}

func (t *CronCreateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a cronCreateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Schedule == "" {
		return json.Marshal("Validation error: schedule is required")
	}
	if a.Prompt == "" {
		return json.Marshal("Validation error: prompt is required")
	}

	recurring := true
	if a.Recurring != nil {
		recurring = *a.Recurring
	}
	durable := false
	if a.Durable != nil {
		durable = *a.Durable
	}

	job, err := t.store.Create(a.Schedule, a.Prompt, recurring, durable)
	if err != nil {
		return json.Marshal(fmt.Sprintf("Error: %s", err))
	}

	desc := cron.HumanSchedule(a.Schedule)
	persistence := "Session-only (not written to disk, destroyed when session ends)"
	if durable {
		persistence = "Persisted to .codebot/scheduled_tasks.json"
	}

	if recurring {
		return json.Marshal(fmt.Sprintf(
			"Scheduled recurring job %s (%s). %s. Auto-expires after 3 days. Use cron_delete to cancel sooner.",
			job.ID, desc, persistence))
	}
	return json.Marshal(fmt.Sprintf(
		"Scheduled one-shot task %s (%s). %s. It will fire once then auto-delete.",
		job.ID, desc, persistence))
}

// ---------------------------------------------------------------------------
// CronDeleteTool
// ---------------------------------------------------------------------------

// CronDeleteTool deletes a cron job by ID.
type CronDeleteTool struct{ store *cron.Store }

func (t *CronDeleteTool) Name() string  { return "cron_delete" }
func (t *CronDeleteTool) Label() string { return "Delete Cron Job" }
func (t *CronDeleteTool) Description() string {
	return `Delete a scheduled cron job by its ID.`
}
func (t *CronDeleteTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("The cron job ID to delete")).Required(),
	)
}

func (t *CronDeleteTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.ID == "" {
		return json.Marshal("Validation error: id is required")
	}

	if err := t.store.Delete(a.ID); err != nil {
		return json.Marshal(fmt.Sprintf("Error: %s", err))
	}
	return json.Marshal(fmt.Sprintf("Cancelled job %s.", a.ID))
}

// ---------------------------------------------------------------------------
// CronListTool
// ---------------------------------------------------------------------------

// CronListTool lists all cron jobs.
type CronListTool struct{ store *cron.Store }

func (t *CronListTool) Name() string  { return "cron_list" }
func (t *CronListTool) Label() string { return "List Cron Jobs" }
func (t *CronListTool) Description() string {
	return `List all scheduled cron jobs with their ID, schedule, and prompt.`
}
func (t *CronListTool) Schema() map[string]any {
	return schema.Object()
}

func (t *CronListTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	jobs := t.store.List()
	if len(jobs) == 0 {
		return json.Marshal("No scheduled jobs.")
	}

	var sb strings.Builder
	for _, j := range jobs {
		mode := "recurring"
		if !j.Recurring {
			mode = "one-shot"
		}
		durableTag := ""
		if !j.Durable {
			durableTag = " [session-only]"
		}
		desc := cron.HumanSchedule(j.Schedule)
		fmt.Fprintf(&sb, "%s — %s (%s)%s: %s\n",
			j.ID, desc, mode, durableTag, cronTruncate(j.Prompt, 80))
	}
	return json.Marshal(strings.TrimRight(sb.String(), "\n"))
}

// cronTruncate shortens s to maxLen, appending "..." if truncated.
func cronTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
