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
	return `Schedule a prompt to be enqueued at a future time. Use for both recurring schedules and one-shot reminders.

Uses standard 5-field cron in the user's local timezone: minute hour day-of-month month day-of-week.
"0 9 * * *" means 9am local — no timezone conversion needed.
Also accepts Go durations (e.g. "5m", "1h") for simple intervals.

## One-shot tasks (recurring: false)
For "remind me at X" or "at <time>, do Y" requests — fire once then auto-delete.
Pin minute/hour/day-of-month/month to specific values:
  "remind me at 2:30pm today to check the deploy" → cron: "30 14 <today_dom> <today_month> *", recurring: false
  "tomorrow morning, run the smoke test" → cron: "57 8 <tomorrow_dom> <tomorrow_month> *", recurring: false

## Recurring jobs (recurring: true, the default)
For "every N minutes" / "every hour" / "weekdays at 9am" requests:
  "*/5 * * * *" (every 5 min), "0 * * * *" (hourly), "0 9 * * 1-5" (weekdays at 9am local)

## Avoid the :00 and :30 minute marks when the task allows it
When the user's request is approximate, pick a minute that is NOT 0 or 30:
  "every morning around 9" → "57 8 * * *" or "3 9 * * *" (not "0 9 * * *")
  "hourly" → "7 * * * *" (not "0 * * * *")
Only use minute 0 or 30 when the user names that exact time and clearly means it.

## Session-only
Jobs live only in this session — nothing is written to disk, and the job is gone when the session exits.

## Runtime behavior
Jobs only fire while the REPL is idle (not mid-query). The scheduler adds a small deterministic jitter:
recurring tasks fire up to 10% of their period late (max 15 min).
Picking an off-minute is still the bigger lever.
Recurring tasks auto-expire after 3 days — they fire one final time, then are deleted.
Tell the user about the 3-day limit when scheduling recurring jobs.
Returns a job ID you can pass to cron_delete.`
}

// Schema exposes cron, prompt, recurring to the LLM.
// The durable parameter is intentionally omitted from the LLM schema
// but accepted internally (used by /loop command).
func (t *CronCreateTool) Schema() map[string]any {
	obj := schema.Object(
		schema.Property("cron", schema.String(
			`Standard 5-field cron expression in local time: "M H DoM Mon DoW" (e.g. "*/5 * * * *" = every 5 minutes, "30 14 28 2 *" = Feb 28 at 2:30pm local once). Also accepts Go durations (e.g. "5m", "1h").`,
		)).Required(),
		schema.Property("prompt", schema.String("The prompt to enqueue at each fire time.")).Required(),
		schema.Property("recurring", schema.Bool(
			`true (default) = fire on every cron match until deleted or auto-expired after 3 days. false = fire once at the next match, then auto-delete. Use false for "remind me at X" one-shot requests with pinned minute/hour/dom/month.`,
		)),
	)
	obj["additionalProperties"] = false
	return obj
}

// Store returns the underlying cron.Store.
func (t *CronCreateTool) Store() *cron.Store { return t.store }

type cronCreateArgs struct {
	Cron      string `json:"cron"`
	Prompt    string `json:"prompt"`
	Recurring *bool  `json:"recurring,omitempty"`
	Durable   *bool  `json:"durable,omitempty"` // hidden from LLM, default false
}

func (t *CronCreateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a cronCreateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Cron == "" {
		return json.Marshal("Validation error: cron is required")
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

	job, err := t.store.Create(a.Cron, a.Prompt, recurring, durable)
	if err != nil {
		return json.Marshal(fmt.Sprintf("Error: %s", err))
	}

	desc := cron.HumanSchedule(a.Cron)
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
	return `Cancel a cron job previously scheduled with cron_create. Removes it from the in-memory session store.`
}
func (t *CronDeleteTool) Schema() map[string]any {
	obj := schema.Object(
		schema.Property("id", schema.String("Job ID returned by cron_create.")).Required(),
	)
	obj["additionalProperties"] = false
	return obj
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
	return `List all cron jobs scheduled via cron_create in this session.`
}
func (t *CronListTool) Schema() map[string]any {
	obj := schema.Object()
	obj["additionalProperties"] = false
	return obj
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
