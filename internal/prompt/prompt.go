package prompt

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// --- Shared prompt sections -------------------------------------------------
//
// Agent-agnostic guidance baked into the universal base block. Changing any of
// them invalidates the prompt cache for the whole session — keep edits
// intentional.

const doingTasksInstructions = `## Doing tasks
- Read the relevant code before changing it. Keep changes to what the task needs: no unrequested refactors, features, comments, or abstractions.
- Validate only at system boundaries (user input, external APIs, I/O). Don't add fallbacks for cases that can't happen or keep compatibility shims — change the code and its callers.
- Prefer editing existing files to creating new ones.
- When something fails, read the error and check your assumptions before changing approach; don't repeat an action unchanged.
- If the user denies a tool call and the reason isn't clear, ask what they'd prefer instead of retrying it.
- Don't introduce security vulnerabilities such as command injection or XSS, and fix any you notice in code you wrote.`

const usingToolsInstructions = `## Using tools
- Prefer the dedicated tools to shell equivalents: read instead of cat/head/tail, edit instead of sed/awk, write instead of shell redirection, glob and grep instead of find/grep/rg. Use bash for work that needs a shell.
- Put independent tool calls in the same response so they run in parallel; make calls one after another only when a call needs an earlier result.`

const systemConventionsInstructions = `## System reminders
Messages may contain <system-reminder> blocks. The harness adds them as context; they are not written by the user.`

const communicationInstructions = `## Communication
Be concise and direct. Lead with the answer or the action, skip preamble and restating the request, and don't narrate routine steps. Spend words on decisions the user needs to make, blockers, and results.`

// identityPreamble opens system block 1.
const identityPreamble = `You are an expert coding assistant working in the user's terminal, with direct access to the filesystem and shell. Your replies are visible to the user.`

// buildIdentityBlock returns system block 1: identity, environment, and the
// shared conventions. Tools are described by their specs, never listed here.
func buildIdentityBlock(cwd string) string {
	var b strings.Builder
	b.WriteString(identityPreamble)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "## Environment\n- Working directory: %s\n- OS: %s/%s\n- Today's date: %s\n\n",
		cwd, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"))
	b.WriteString(doingTasksInstructions)
	b.WriteString("\n\n")
	b.WriteString(usingToolsInstructions)
	b.WriteString("\n\n")
	b.WriteString(systemConventionsInstructions)
	b.WriteString("\n\n")
	b.WriteString(communicationInstructions)
	return b.String()
}

// buildInstructionsBlock returns system block 2: auto-memory hints and the
// project-scoped context (skill listing, AGENTS.md, MEMORY.md,
// APPEND_SYSTEM.md).
func buildInstructionsBlock(ctx ContextFiles, skills string) string {
	parts := []string{memoryInstructions(ctx.MemoryDir)}
	parts = append(parts, buildProjectContext(ctx, skills)...)
	return strings.Join(parts, "\n\n")
}

// buildProjectContext renders the workspace-scoped sections of block 2:
// skill catalog, AGENTS.md, MEMORY.md, and APPEND_SYSTEM.md. Order is fixed so
// the block's bytes depend only on content, never on call order.
func buildProjectContext(ctx ContextFiles, skills string) []string {
	var parts []string
	if skills != "" {
		parts = append(parts, "## Skills\n"+skills)
	}
	if ctx.Agents != "" {
		parts = append(parts, "## Project Context\n"+ctx.Agents)
	}
	parts = append(parts, buildMemorySection(ctx))
	if ctx.SystemAppend != "" {
		parts = append(parts, ctx.SystemAppend)
	}
	return parts
}

func buildMemorySection(ctx ContextFiles) string {
	body := ctx.Memory
	if body == "" {
		// The auto-memory instructions promise MEMORY.md is always in
		// context. Without this placeholder the model sees the promise and
		// tries to Read the file, which is ENOENT before anything is saved.
		body = "Your MEMORY.md is currently empty. When you save new memories, they will appear here."
	}
	return "## Memory\nContents of " + filepath.Join(ctx.MemoryDir, "MEMORY.md") +
		" (auto-memory, persists across conversations):\n\n" + body +
		"\n\nMemories reflect what was true when they were written. Before relying on one, verify that the files, functions, or flags it mentions still exist — a memory saying X exists is not the same as X existing now."
}

// Identity returns system block 1. A SystemOverride replaces the whole
// prompt, so there is no identity block to build.
func Identity(cwd string, ctx ContextFiles) string {
	if ctx.SystemOverride != "" {
		return ""
	}
	return buildIdentityBlock(cwd)
}

// Instructions returns system block 2: the SYSTEM.md override verbatim when
// present, otherwise the composed instructions block. skills is the rendered
// skill listing.
func Instructions(ctx ContextFiles, skills string) string {
	if ctx.SystemOverride != "" {
		return ctx.SystemOverride
	}
	return buildInstructionsBlock(ctx, skills)
}

// Suggestion is the instruction appended as a user message to generate
// a prompt suggestion after the agent completes a turn.
const Suggestion = `[SUGGESTION MODE: Suggest what the user might naturally type next.]

FIRST: Look at the user's recent messages and original request.

Your job is to predict what THEY would type - not what you think they should do.

THE TEST: Would they think "I was just about to type that"?

EXAMPLES:
User asked "fix the bug and run tests", bug is fixed → "run the tests"
After code written → "try it out"
You offered options → suggest the one the user would likely pick, based on conversation
You asked whether to continue → "yes" or "go ahead"
Task complete, obvious follow-up → "commit this" or "push it"
After error or misunderstanding → silence (let them assess/correct)

Be specific: "run the tests" beats "continue".

NEVER SUGGEST:
- Evaluative ("looks good", "thanks")
- Questions ("what about...?")
- Your own voice ("Let me...", "I'll...", "Here's...")
- New ideas they didn't ask about
- Multiple sentences

If the next step isn't obvious from what the user said, output exactly "NONE" (no quotes, nothing else).

Format: 2-12 words, match the user's style. Or nothing.

Reply with ONLY the suggestion, no quotes or explanation.`
