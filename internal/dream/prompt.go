package dream

import "fmt"

// dreamSystemPrompt fixes the dream sub-agent's role and tool boundary. The
// constraints stated here are only a hint — the hard enforcement is in
// pathguard.
const dreamSystemPrompt = `You are a memory-consolidation agent running in the background while the user is away. Your only job is to reorganize the project's auto-memory directory: merge, prune, and index what past sessions learned.

You have read-only exploration tools (read, grep, glob, ls) plus write and edit that only work inside the memory directory. There is no bash. Do not attempt to modify anything outside the memory directory. Work autonomously — nobody will answer questions. Finish with a brief summary of what changed.`

// buildConsolidationPrompt builds the four-phase consolidation task prompt.
// It mirrors CC's consolidationPrompt.ts but adapts to codebot's memory
// shape: MEMORY.md is an index injected into every turn's system-reminder and
// truncated past 200 lines; there is no daily-logs directory; transcripts are
// <date>_<id>.jsonl files under SessionsDir.
func buildConsolidationPrompt(memoryDir, transcriptDir string, sessionsTouched int) string {
	return fmt.Sprintf(`# Dream: Memory Consolidation

You are performing a dream — a reflective pass over your memory files. Synthesize what recent sessions learned into durable, well-organized memories so future sessions orient quickly.

Memory directory: %s (already exists — write files directly)
Session transcripts: %s (large *.jsonl files — grep narrowly, never read whole files)
Sessions active since the last consolidation: %d

## Phase 1 — Orient

- ls the memory directory to see what exists
- Read MEMORY.md to understand the current index
- Skim existing topic files so you improve them instead of duplicating them

## Phase 2 — Gather recent signal

Look for new information worth persisting, in priority order:

1. Existing memories that drifted — facts contradicted by what the codebase shows now
2. Transcript search — grep the *.jsonl transcripts for narrow terms you already suspect matter

Do not exhaustively read transcripts. Look only for things you already suspect are important.

## Phase 3 — Consolidate

- Merge new signal INTO existing topic files rather than creating near-duplicates
- Convert relative dates ("yesterday", "last week") to absolute dates so they stay interpretable
- Delete facts that have been disproven — fix them at the source
- Keep detailed notes in topic files (e.g. debugging.md, patterns.md), never in MEMORY.md

## Phase 4 — Prune and index

MEMORY.md is injected into every conversation and hard-truncated at 200 lines — it must stay a concise INDEX, never a dump:

- One line per entry pointing at a topic file, with a one-line hook
- Remove pointers to memories that are stale, wrong, or superseded
- Resolve contradictions — if two files disagree, fix the wrong one

Return a brief summary of what you consolidated, updated, or pruned. If memories are already tight, say so and change nothing.`,
		memoryDir, transcriptDir, sessionsTouched)
}
