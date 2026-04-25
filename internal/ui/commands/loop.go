package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/cron"
	"github.com/voocel/codebot/internal/ui/tui"
)

// Loop constructs the /loop command which schedules recurring prompts via the
// session-scoped cron store. With no args it prints usage; subcommands list/
// stop manage existing jobs.
func Loop(store *cron.Store) Command {
	return NewSimple(Spec{
		Name: "loop", Usage: "/loop <interval|cron> <prompt>",
		Description: "Schedule recurring prompts",
		Category:    "session", Kind: KindBuiltin,
	}, func(inv Invocation) tea.Cmd {
		if store == nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Cron scheduling is not available."))
		}

		args := strings.TrimSpace(inv.RawArgs)
		if args == "" {
			return tui.SendCommandResult(tui.CommandStyle.Render(
				"Usage:\n  /loop <interval|cron> <prompt>  — create a recurring job\n  /loop list                       — list all jobs\n  /loop stop <id|all>              — stop a job or all jobs\n\nExamples:\n  /loop 5m run tests\n  /loop \"*/10 * * * *\" check build status"))
		}

		if args == "list" {
			return loopList(store)
		}

		if strings.HasPrefix(args, "stop ") || args == "stop" {
			target := strings.TrimSpace(strings.TrimPrefix(args, "stop"))
			return loopStop(store, target)
		}

		schedule, prompt := parseLoopArgs(args)
		if schedule == "" || prompt == "" {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Invalid syntax. Usage: /loop <interval|cron> <prompt>"))
		}

		job, err := store.Create(schedule, prompt, true, true)
		if err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(fmt.Sprintf("Failed to create job: %s", err)))
		}

		desc := cron.HumanSchedule(schedule)
		return tui.SendCommandResult(tui.SystemMsgStyle.Render(
			fmt.Sprintf("Scheduled job %s (%s): %q\nNext fire: %s",
				job.ID, desc, prompt, job.NextFire().Format("15:04:05"))))
	})
}

func loopList(store *cron.Store) tea.Cmd {
	jobs := store.List()
	if len(jobs) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No scheduled jobs."))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduled jobs (%d):\n", len(jobs))
	for _, j := range jobs {
		mode := "recurring"
		if !j.Recurring {
			mode = "one-shot"
		}
		desc := cron.HumanSchedule(j.Schedule)
		fmt.Fprintf(&sb, "  %s  %-20s [%s]  %q  (next: %s)\n",
			j.ID, desc, mode, j.Prompt, j.NextFire().Format("15:04:05"))
	}
	sb.WriteString("\nUse /loop stop <id> to remove a job.")
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func loopStop(store *cron.Store, target string) tea.Cmd {
	if target == "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /loop stop <id|all>"))
	}
	if target == "all" {
		n := store.DeleteAll()
		return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf("Stopped all %d jobs.", n)))
	}
	if err := store.Delete(target); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf("Job %s stopped.", target)))
}

// parseLoopArgs extracts schedule and prompt from /loop args.
// Supports: "5m run tests", "\"*/5 * * * *\" check build"
func parseLoopArgs(args string) (schedule, prompt string) {
	// Quoted schedule: "/loop "*/5 * * * *" check build"
	if strings.HasPrefix(args, "\"") || strings.HasPrefix(args, "'") {
		quote := args[0]
		end := strings.IndexByte(args[1:], quote)
		if end >= 0 {
			schedule = args[1 : end+1]
			prompt = strings.TrimSpace(args[end+2:])
			return schedule, prompt
		}
	}

	// Try to detect cron expression (5 fields before the prompt).
	// A cron field can only contain: digits, *, /, -, comma.
	fields := strings.Fields(args)
	if len(fields) >= 6 && looksLikeCronFields(fields[:5]) {
		return strings.Join(fields[:5], " "), strings.Join(fields[5:], " ")
	}

	// Simple interval: first token is schedule, rest is prompt.
	if len(fields) >= 2 {
		return fields[0], strings.Join(fields[1:], " ")
	}

	return "", ""
}

func looksLikeCronFields(fields []string) bool {
	for _, f := range fields {
		for _, ch := range f {
			if !((ch >= '0' && ch <= '9') || ch == '*' || ch == '/' || ch == '-' || ch == ',') {
				return false
			}
		}
	}
	return true
}
