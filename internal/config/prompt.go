package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/skill"
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

	var identityBody strings.Builder
	fmt.Fprintf(&identityBody, `You are an expert coding assistant with direct access to the filesystem and shell.

## Environment
- Working directory: %s
- OS: %s/%s
- Date: %s
`, cwd, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"))
	identity = identityBody.String()

	var toolsBody strings.Builder
	hasTaskCreate := false
	hasTaskUpdate := false
	hasTaskList := false
	if len(tools) > 0 {
		fmt.Fprintf(&toolsBody, "## Tools\nYou have %d tools:\n", len(tools))
		for _, t := range tools {
			fmt.Fprintf(&toolsBody, "- **%s**: %s\n", t.Name, t.Description)
			switch t.Name {
			case "task_create":
				hasTaskCreate = true
			case "task_update":
				hasTaskUpdate = true
			case "task_list":
				hasTaskList = true
			}
		}
	}
	taskManagementInstructions := ""
	if hasTaskCreate && hasTaskUpdate && hasTaskList {
		taskManagementInstructions = `## Task Management
Break down and manage your work with task_create, task_update, and task_list.
- Use these tools proactively for non-trivial work with multiple steps or requirements
- Do not create a task list for a single trivial or purely conversational request
- Break larger requests into multiple specific tasks instead of one broad task
- Mark a task in_progress before starting it
- Mark a task completed immediately after fully finishing it
- Do not batch multiple completions together
- Keep at most one task in_progress at a time
- Check task_list before creating more tasks if a relevant task may already exist
- After completing a task, call task_list to find the next pending or unblocked task`
	}
	autoMemoryInstructions := BuildAutoMemoryInstructions(ctx.MemoryDir)
	var instructionParts []string
	if toolsBody.Len() > 0 {
		instructionParts = append(instructionParts, toolsBody.String())
	}
	if taskManagementInstructions != "" {
		instructionParts = append(instructionParts, taskManagementInstructions)
	}
	if autoMemoryInstructions != "" {
		instructionParts = append(instructionParts, autoMemoryInstructions)
	}
	instructions = strings.Join(instructionParts, "\n\n")
	return
}

// BuildReminders extracts skills and context files into <system-reminder>
// wrapped text fragments for injection into user messages.
// Returns nil when there are no reminders to inject.
func BuildReminders(ctx ContextFiles, skills []skill.Spec) []string {
	var reminders []string
	if skillBlock := skill.RenderListing(skills, skill.DefaultListingOptions()); skillBlock != "" {
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
	if len(reminders) == 0 {
		return nil
	}
	return reminders
}
