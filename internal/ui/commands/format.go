package commands

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/apperr"
	"github.com/voocel/codebot/internal/ui/tui"
)

// FormatBool maps a boolean to a human-readable yes/no for status panels.
func FormatBool(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// FormatReminderCounts renders runtime reminder counts in a stable order.
func FormatReminderCounts(counts map[agent.RuntimeReminderKind]int) string {
	order := []agent.RuntimeReminderKind{
		agent.ReminderRepeatToolCall,
		agent.ReminderPostStopValidation,
		agent.ReminderTaskManagement,
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range order {
		if count := counts[kind]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", kind, count))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// FormatCompactionCounts renders compaction kind tallies.
func FormatCompactionCounts(counts map[agent.CompactionKind]int) string {
	order := []agent.CompactionKind{
		agent.CompactionKindMicro,
		agent.CompactionKindTrim,
		agent.CompactionKindPrune,
		agent.CompactionKindFull,
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range order {
		if count := counts[kind]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", kind, count))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// FormatCompactionSavings renders the per-kind token savings totals.
func FormatCompactionSavings(savings map[agent.CompactionKind]int) string {
	order := []agent.CompactionKind{
		agent.CompactionKindMicro,
		agent.CompactionKindTrim,
		agent.CompactionKindPrune,
		agent.CompactionKindFull,
	}
	parts := make([]string, 0, len(savings))
	for _, kind := range order {
		if saved := savings[kind]; saved > 0 {
			parts = append(parts, fmt.Sprintf("%s=%s", kind, tui.FormatTokens(saved)))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// FormatErrorCounts renders error kind tallies in a stable order.
func FormatErrorCounts(counts map[apperr.Kind]int) string {
	order := []apperr.Kind{
		apperr.KindCanceled,
		apperr.KindConfig,
		apperr.KindPermission,
		apperr.KindProvider,
		apperr.KindSession,
		apperr.KindToolInput,
		apperr.KindToolExec,
		apperr.KindUnknown,
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range order {
		if count := counts[kind]; count > 0 {
			label := string(kind)
			if kind == apperr.KindUnknown {
				label = "unknown"
			}
			parts = append(parts, fmt.Sprintf("%s=%d", label, count))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// FormatRecentToolCalls renders recent tool call snapshots one per line.
func FormatRecentToolCalls(calls []agent.ToolCallSnapshot) []string {
	if len(calls) == 0 {
		return nil
	}
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		status := "ok"
		if !call.Success {
			status = "error"
		}
		argsHash := call.ArgsHash
		if argsHash == "" {
			argsHash = "-"
		}
		lines = append(lines, fmt.Sprintf("%s  %-12s %-5s args:%s",
			call.Timestamp.Format("15:04:05"),
			call.Tool,
			status,
			argsHash,
		))
	}
	return lines
}

// FormatRecentErrors renders recent error snapshots one per line.
func FormatRecentErrors(errors []agent.ErrorSnapshot) []string {
	if len(errors) == 0 {
		return nil
	}
	lines := make([]string, 0, len(errors))
	for _, snapshot := range errors {
		kind := string(snapshot.Kind)
		if snapshot.Kind == apperr.KindUnknown {
			kind = "unknown"
		}
		line := fmt.Sprintf("%s  %-12s %s",
			snapshot.Timestamp.Format("15:04:05"),
			kind,
			snapshot.Message,
		)
		if snapshot.Detail != "" {
			line += " | " + snapshot.Detail
		}
		lines = append(lines, line)
	}
	return lines
}

// FormatLastReminder renders the most recent runtime reminder snapshot.
func FormatLastReminder(snapshot agent.ReminderSnapshot, ok bool) string {
	if !ok {
		return "(none)"
	}
	return fmt.Sprintf("%s via %s at %s", snapshot.Kind, snapshot.Mode, snapshot.Timestamp.Format("15:04:05"))
}

// FormatLastCompaction renders the most recent compaction snapshot.
func FormatLastCompaction(snapshot agent.CompactionSnapshot, ok bool) string {
	if !ok {
		return "(none)"
	}
	status := "no-op"
	if snapshot.Changed {
		status = fmt.Sprintf("changed, %s -> %s", tui.FormatTokens(snapshot.TokensBefore), tui.FormatTokens(snapshot.TokensAfter))
	}
	label := fmt.Sprintf("%s / %s", snapshot.Kind, snapshot.Reason)
	if strategy := PrettyCompactionStrategy(snapshot.Strategy); strategy != "" {
		label += " / " + strategy
	}
	if snapshot.CompactedCount > 0 || snapshot.KeptCount > 0 || snapshot.SplitTurn {
		var detailParts []string
		if snapshot.CompactedCount > 0 {
			detailParts = append(detailParts, fmt.Sprintf("compacted=%d", snapshot.CompactedCount))
		}
		if snapshot.KeptCount > 0 {
			detailParts = append(detailParts, fmt.Sprintf("kept=%d", snapshot.KeptCount))
		}
		if snapshot.SplitTurn {
			detailParts = append(detailParts, "split-turn")
		}
		label += " / " + strings.Join(detailParts, ",")
	}
	return fmt.Sprintf("%s, %s, at %s",
		label,
		status,
		snapshot.Timestamp.Format("15:04:05"),
	)
}

// FormatRunSummary renders the most recent agentcore run summary.
func FormatRunSummary(summary agentcore.RunSummary, ok bool) string {
	if !ok {
		return "(none)"
	}
	return fmt.Sprintf("reason=%s, turns=%d, tool_calls=%d, tool_errors=%d",
		summary.EndReason,
		summary.TurnCount,
		summary.ToolCalls,
		summary.ToolErrors,
	)
}

// FormatContextScope translates a snapshot scope token into a human label.
func FormatContextScope(scope string) string {
	switch scope {
	case "baseline":
		return "baseline runtime"
	case "projected":
		return "projected view"
	case "committed":
		return "committed view"
	case "recovered":
		return "overflow recovery"
	default:
		if scope == "" {
			return "(unknown)"
		}
		return scope
	}
}

// FormatContextRewriteDetails describes the last context rewrite metadata.
func FormatContextRewriteDetails(snapshot *agentcore.ContextSnapshot) string {
	if snapshot == nil {
		return "(none)"
	}
	var parts []string
	if snapshot.LastCompactedCount > 0 {
		parts = append(parts, fmt.Sprintf("compacted=%d", snapshot.LastCompactedCount))
	}
	if snapshot.LastKeptCount > 0 {
		parts = append(parts, fmt.Sprintf("kept=%d", snapshot.LastKeptCount))
	}
	if snapshot.LastSplitTurn {
		parts = append(parts, "split-turn")
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// PrettyCompactionStrategy maps an internal compaction strategy id to a
// human-readable label, returning empty when no mapping is registered.
func PrettyCompactionStrategy(name string) string {
	switch name {
	case "tool_result_microcompact":
		return "tool-result microcompact"
	case "light_trim":
		return "light trim"
	case "full_summary":
		return "full summary"
	default:
		return ""
	}
}
