package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ToolInfo describes a tool for system prompt generation.
// Decoupled from agentcore.Tool to avoid package dependency.
type ToolInfo struct {
	Name        string
	Description string
}

// BuildSystemBlockTexts returns the system prompt split into two stable segments:
//   - identity: role description + environment info
//   - instructions: tool descriptions + guidelines
//
// Skills and context files are NOT included — they go into user-message reminders
// via BuildReminders for better cache stability.
//
// When ctx.SystemOverride is set (SYSTEM.md), it replaces the default prompt entirely.
// In this case identity is empty and instructions contains the full override.
func BuildSystemBlockTexts(cwd string, ctx ContextFiles, tools []ToolInfo) (identity, instructions string) {
	if ctx.SystemOverride != "" {
		return "", ctx.SystemOverride
	}

	var id strings.Builder
	fmt.Fprintf(&id, `You are an expert coding assistant with direct access to the filesystem and shell.

## Environment
- Working directory: %s
- OS: %s/%s
- Date: %s
`, cwd, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"))
	identity = id.String()

	var inst strings.Builder
	if len(tools) > 0 {
		fmt.Fprintf(&inst, "## Tools\nYou have %d tools:\n", len(tools))
		for _, t := range tools {
			fmt.Fprintf(&inst, "- **%s**: %s\n", t.Name, t.Description)
		}
	}
	if memInst := BuildAutoMemoryInstructions(ctx.MemoryDir); memInst != "" {
		fmt.Fprintf(&inst, "\n%s\n", memInst)
	}
	instructions = inst.String()
	return
}

// BuildReminders extracts skills and context files into <system-reminder>
// wrapped text fragments for injection into user messages.
// Returns nil when there are no reminders to inject.
func BuildReminders(ctx ContextFiles, skills []Skill) []string {
	var reminders []string
	if skillBlock := FormatSkillsForPrompt(skills); skillBlock != "" {
		reminders = append(reminders, "<system-reminder>\n## Skills\n"+skillBlock+"\n</system-reminder>")
	}
	if ctx.Agents != "" {
		reminders = append(reminders, "<system-reminder>\n## Project Context\n"+ctx.Agents+"\n</system-reminder>")
	}
	if ctx.SystemAppend != "" {
		reminders = append(reminders, "<system-reminder>\n"+ctx.SystemAppend+"\n</system-reminder>")
	}
	if ctx.Memory != "" {
		memPath := filepath.Join(ctx.MemoryDir, "MEMORY.md")
		reminders = append(reminders, "<system-reminder>\nContents of "+memPath+" (auto-memory, persists across conversations):\n\n"+ctx.Memory+"\n</system-reminder>")
	}
	return reminders
}
