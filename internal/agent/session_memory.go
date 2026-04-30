package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/codebot/internal/config"
)

// SessionMemory is a project-scoped, markdown-formatted running summary of the
// user's collaboration with the agent. It is updated in the background after
// high-signal turns so that autoCompact and session resume can inherit
// accumulated context without re-summarizing the full transcript.

const (
	// First extraction triggers once prompt tokens cross this threshold.
	sessionMemoryInitTokens = 10_000

	// Minimum token delta between extractions.
	sessionMemoryUpdateTokens = 5_000

	// Bounds one extraction's model call.
	sessionMemoryExtractionTimeout = 90 * time.Second

	// An extraction slot held past this point is treated as dead (leaked
	// goroutine, stuck I/O) and reclaimed. Must exceed the timeout with a
	// safety margin so a still-running goroutine doesn't race a fresh one
	// against ChatModel.Generate, which is not promised to be concurrent-safe.
	sessionMemoryStaleAfter = 2 * sessionMemoryExtractionTimeout

	// Caps the response we accept; template fills to ~1k, 3k gives headroom.
	sessionMemoryMaxExtractionTokens = 3000
)

// sessionMemoryTemplate is the initial scaffold written on first extraction
// when no memory file exists. Section headers are what the model reads and
// updates in place; italic body text is guidance for the model.
const sessionMemoryTemplate = `# Session Memory

## Current State
_In-progress work and its status. One paragraph._

## Task Specification
_Goals, constraints, acceptance criteria, user preferences._

## Files and Functions
_Paths touched, with a one-line purpose each. Group by directory._

## Workflow
_Ongoing multi-step tactic (if any). Omit if nothing is mid-flight._

## Errors & Corrections
_What went wrong, what fix landed, what to avoid next time._

## Learnings
_Non-obvious facts about the codebase, the user's habits, project idioms._

## Worklog
_Chronological bullet list of high-signal turns. Most recent last._
`

// sessionMemoryUpdatePrompt asks the model to re-emit the memory body with the
// latest information folded in. The previous content is included so the model
// can preserve structure and update only what changed.
const sessionMemoryUpdatePrompt = `You maintain a running session memory for this coding collaboration. Update the memory below to reflect the conversation so far.

RULES:
- Preserve every section header exactly, even if empty.
- Replace italic guidance lines with real content as sections get populated.
- Do not invent facts. If a section has no information, leave its guidance line.
- Keep each section concise (aim for 200 words max per section).
- Reply with ONLY the updated markdown body. No preamble, no code fences, no commentary.

<current-session-memory>
%s
</current-session-memory>`

// sessionMemoryState tracks extraction bookkeeping per session. Embedded in
// Session (not module-global) so concurrent sessions don't cross-contaminate.
type sessionMemoryState struct {
	mu sync.Mutex

	initialized         bool
	tokensAtLast        int
	extractionStartedAt time.Time // zero when idle
}

// SessionMemory is the on-disk shape of a memory file. We write the body as
// plain markdown (no frontmatter) so the file is readable and editable by a
// human. UpdatedAt comes from the filesystem mtime, not a stored field.
type SessionMemory struct {
	Content   string
	UpdatedAt time.Time
}

// loadSessionMemory reads the on-disk memory for this session's project. It
// returns (nil, nil) when the file does not exist — callers should fall back
// to the default template.
func (s *Session) loadSessionMemory() (*SessionMemory, error) {
	path := config.SessionMemoryPath(s.cwd)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session memory: %w", err)
	}
	return &SessionMemory{Content: string(data), UpdatedAt: fileModTime(path)}, nil
}

// saveSessionMemory writes the memory atomically. A temp file + rename keeps
// a concurrent reader from seeing a half-written file.
func (s *Session) saveSessionMemory(content string) error {
	path := config.SessionMemoryPath(s.cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir session memory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// maybeExtractSessionMemory is the gate that decides whether to kick off a
// background extraction. Called after every assistant message_end. The three
// guard conditions are:
//  1. an extraction is not already in-flight (stale-aware);
//  2. the session has crossed the init or update token threshold;
//  3. the last message is safe to summarize up to (no pending tool_use).
//
// All decisions are cheap — the actual model call happens on a goroutine.
func (p *sessionPersistence) maybeExtractSessionMemory() {
	s := p.session

	msgs := s.agent.Messages()
	if !isSafeSummaryBoundary(msgs) {
		// Trailing assistant tool_use awaiting results — postpone.
		return
	}

	total := agentctx.EstimateTotal(msgs)

	s.sessionMemory.mu.Lock()
	// Stale cleanup: if a prior extraction started but never completed,
	// release the lock so we can try again.
	if !s.sessionMemory.extractionStartedAt.IsZero() &&
		time.Since(s.sessionMemory.extractionStartedAt) > sessionMemoryStaleAfter {
		s.sessionMemory.extractionStartedAt = time.Time{}
	}
	if !s.sessionMemory.extractionStartedAt.IsZero() {
		s.sessionMemory.mu.Unlock()
		return
	}

	initNeeded := !s.sessionMemory.initialized && total >= sessionMemoryInitTokens
	delta := total - s.sessionMemory.tokensAtLast
	updateNeeded := s.sessionMemory.initialized && delta >= sessionMemoryUpdateTokens
	if !initNeeded && !updateNeeded {
		s.sessionMemory.mu.Unlock()
		return
	}

	s.sessionMemory.extractionStartedAt = time.Now()
	gen := s.generation
	s.sessionMemory.mu.Unlock()

	go s.runSessionMemoryExtraction(gen, total)
}

// runSessionMemoryExtraction is the background worker. It must only mutate
// sessionMemoryState through the guarded helpers below so that a stale
// goroutine from a swapped-out session cannot corrupt the current one.
func (s *Session) runSessionMemoryExtraction(gen uint64, tokens int) {
	defer func() {
		s.sessionMemory.mu.Lock()
		s.sessionMemory.extractionStartedAt = time.Time{}
		s.sessionMemory.mu.Unlock()
	}()

	// Bail if the session was switched / closed while we were queued.
	if !s.matchesGeneration(gen) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionMemoryExtractionTimeout)
	defer cancel()

	existing, _ := s.loadSessionMemory()
	body := sessionMemoryTemplate
	if existing != nil && strings.TrimSpace(existing.Content) != "" {
		body = existing.Content
	}

	prompt := fmt.Sprintf(sessionMemoryUpdatePrompt, body)
	resp, err := s.ephemeralQuery(ctx, prompt,
		agentcore.WithMaxTokens(sessionMemoryMaxExtractionTokens),
	)
	if err != nil {
		s.emit(SessionEvent{Type: SEError, Error: fmt.Errorf("session memory extract: %w", err)})
		return
	}

	content := strings.TrimSpace(resp.Message.TextContent())
	if content == "" {
		return
	}
	content = stripCodeFence(content)

	if err := s.saveSessionMemory(content); err != nil {
		s.emit(SessionEvent{Type: SEError, Error: fmt.Errorf("session memory save: %w", err)})
		return
	}

	// Re-check generation before committing state so a late-landing goroutine
	// doesn't overwrite a fresh session's bookkeeping.
	if !s.matchesGeneration(gen) {
		return
	}
	s.sessionMemory.mu.Lock()
	s.sessionMemory.initialized = true
	s.sessionMemory.tokensAtLast = tokens
	s.sessionMemory.mu.Unlock()
}

// matchesGeneration reports whether the session has not been switched since
// the background goroutine was dispatched.
func (s *Session) matchesGeneration(gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation == gen
}

// isSafeSummaryBoundary reports whether the current tail of the conversation
// is safe to summarize up to. A trailing assistant message carrying tool_use
// calls whose results have not landed yet would be orphaned by an extraction
// cut there, so we postpone until the tool_result turn lands.
func isSafeSummaryBoundary(msgs []agentcore.AgentMessage) bool {
	if len(msgs) == 0 {
		return false
	}
	last := msgs[len(msgs)-1]
	if last.GetRole() == agentcore.RoleAssistant && last.HasToolCalls() {
		return false
	}
	return true
}

// SessionMemorySeedFn returns a closure suitable for plugging into
// agentcore's SessionMemoryStrategy. The closure reads the project-scoped
// memory file on each compaction attempt and returns the empty string when
// the file is missing or still matches the initial template — both of which
// signal "no useful memory yet, fall through to LLM summarization".
func SessionMemorySeedFn(cwd string) func() (string, error) {
	return func() (string, error) {
		data, err := os.ReadFile(config.SessionMemoryPath(cwd))
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil
			}
			return "", err
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			return "", nil
		}
		if body == strings.TrimSpace(sessionMemoryTemplate) {
			return "", nil
		}
		return body, nil
	}
}

// stripCodeFence removes a surrounding ```...``` if the model wrapped its
// output — a common failure mode despite the "no code fences" instruction.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop opening fence (possibly with language tag).
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
