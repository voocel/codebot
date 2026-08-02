package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	localtools "github.com/voocel/codebot/internal/tools"
)

// MinCompactableResultTokens avoids rewriting small, often stateful results.
const MinCompactableResultTokens = 200

// These results cannot be reconstructed after clearing.
var protectedFromMicrocompact = map[string]struct{}{
	"skill":    {},
	"ask_user": {},
}

func CodebotToolClassifier(name string) bool {
	_, protected := protectedFromMicrocompact[name]
	return !protected
}

// ClearedToolResultMessage preserves the path of output already persisted by
// tools.OutputLimiter. "" selects the default cleared message.
func ClearedToolResultMessage(_ string, original agentcore.Message) string {
	for _, b := range original.Content {
		if b.Type != agentcore.ContentText {
			continue
		}
		if path := localtools.PersistedOutputPath(b.Text); path != "" {
			return agentctx.DefaultClearedToolResult + "\n" + localtools.PersistedPathLabel + path
		}
	}
	return ""
}

func (s *Session) PostSummaryRecoveryHook() agentctx.PostSummaryHook {
	return s.postCompactRecoveryMessages
}

func (s *Session) HandleProjectedRewrite(info agentctx.RewriteEvent) {
	s.handleAutomaticRewrite(info)
}

func (s *Session) HandleOverflowRewrite(info agentctx.RewriteEvent) {
	s.handleAutomaticRewrite(info)
}

// SetRewriteProgress reports strategy execution to the frontend.
func (s *Session) SetRewriteProgress(fn func(strategy string) func()) {
	if engine, ok := s.deps.contextManager.(*agentctx.ContextEngine); ok {
		engine.SetStrategyHook(fn)
	}
}

func (s *Session) handleAutomaticRewrite(info agentctx.RewriteEvent) {
	if !info.Changed {
		return
	}
	kind := compactionKindForStrategy(info.Strategy)
	s.metrics.recordCompactionAttempt(kind)
	s.metrics.recordCompactionResult(kind, true, info.TokensBefore, info.TokensAfter)
	compactedCount, keptCount, splitTurn := 0, 0, false
	if info.Info != nil {
		compactedCount = info.Info.CompactedCount
		keptCount = info.Info.KeptCount
		splitTurn = info.Info.IsSplitTurn
	}
	s.metrics.recordCompactionSnapshot(CompactionSnapshot{
		Kind:           kind,
		Strategy:       info.Strategy,
		Reason:         info.Reason,
		Changed:        true,
		TokensBefore:   info.TokensBefore,
		TokensAfter:    info.TokensAfter,
		CompactedCount: compactedCount,
		KeptCount:      keptCount,
		SplitTurn:      splitTurn,
	})
	s.emit(SessionEvent{
		Type:               SEAutoCompactionEnd,
		CompactionReason:   info.Reason,
		CompactionKind:     kind,
		CompactionStrategy: info.Strategy,
		CompactionChanged:  true,
		TokensBefore:       info.TokensBefore,
		TokensAfter:        info.TokensAfter,
		CompactedCount:     compactedCount,
		KeptCount:          keptCount,
		SplitTurn:          splitTurn,
	})

	// The engine installed this view as the new baseline, so the tail of an
	// explicit /compact applies here too. Info is non-nil only for a summary
	// rewrite — a tool-result commit produces no checkpoint and needs neither
	// step.
	if info.Committed && info.Info != nil {
		if err := persistCompaction(s.persist.currentStore(), info.View); err != nil {
			s.emit(SessionEvent{Type: SEError, Error: err})
		}
		s.reminders.resetSummarized()
	}
}

// CompactionBlocks reports whether a strategy may block on a model call.
func CompactionBlocks(strategy string) bool {
	return compactionKindForStrategy(strategy) == CompactionKindFull
}

func compactionKindForStrategy(name string) CompactionKind {
	switch name {
	case "tool_result_microcompact":
		return CompactionKindMicro
	// light_trim is no longer in the chain — kept so sessions recorded while
	// it was still running replay with the right kind.
	case "light_trim":
		return CompactionKindTrim
	default:
		return CompactionKindFull
	}
}

const (
	postCompactMaxFiles = 5
	// room remains the effective limit.
	postCompactMaxTokens       = 50000
	postCompactMaxTokenPerFile = 5000
	postCompactBytesPerToken   = 4
)

// postCompactRecoveryMessages restores required reminders before optional file
// bodies, without exceeding room.
func (s *Session) postCompactRecoveryMessages(_ context.Context, info agentctx.SummaryInfo, kept []agentcore.AgentMessage, room int) ([]agentcore.AgentMessage, error) {
	left := room
	var tail []agentcore.AgentMessage
	claim := func(name string, msg agentcore.AgentMessage) error {
		cost := agentctx.EstimateTokens(msg)
		if cost > left {
			return fmt.Errorf("post-compact recovery: %s requires %d tokens, only %d available", name, cost, left)
		}
		tail = append(tail, msg)
		left -= cost
		return nil
	}
	if reminder := s.invokedSkillReminderMessage(); reminder != nil {
		if err := claim("invoked-skill reminder", *reminder); err != nil {
			return nil, err
		}
	}
	if preamble := s.prompt.preambleSnapshot(); preamble != "" {
		if err := claim("deferred-tools preamble", injectedUserMsg(preamble)); err != nil {
			return nil, err
		}
	}

	files := readRecentFiles(info, kept, min(postCompactMaxTokens, left))
	return append(files, tail...), nil
}

// readRecentFiles re-reads files referenced by a committed summary rewrite so
// the model can continue editing without an extra Read call. Kept at the
// application layer; only used for explicit committed rewrites, not transient
// projected views.
//
// Strategy:
//   - modified files first (higher priority), then read-only
//   - skip files already in kept messages, plan files, memory files, binaries
//   - max 5 files, budget tokens in total, 5k per file
func readRecentFiles(info agentctx.SummaryInfo, kept []agentcore.AgentMessage, budget int) []agentcore.AgentMessage {
	if budget <= 0 {
		return nil
	}

	var candidates []string
	candidates = append(candidates, info.ModifiedFiles...)
	candidates = append(candidates, info.ReadFiles...)
	if len(candidates) == 0 {
		return nil
	}

	keptFiles := collectReadFilesFromMessages(kept)

	var files []string
	seen := make(map[string]bool)
	for _, f := range candidates {
		if seen[f] || keptFiles[f] || shouldExcludeFromRestore(f) {
			continue
		}
		seen[f] = true
		files = append(files, f)
		if len(files) >= postCompactMaxFiles {
			break
		}
	}
	if len(files) == 0 {
		return nil
	}

	var out []agentcore.AgentMessage
	totalTokens := 0
	for _, path := range files {
		if totalTokens >= budget {
			break
		}

		// Skip binary files — they can't be meaningfully injected as text
		if looksLikeBinary(path) {
			continue
		}

		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(content) == 0 {
			continue
		}

		// Include the wrapper in the budget.
		wrapper := len(fmt.Sprintf("<file-restore path=%q>\n\n</file-restore>", path))
		maxBytes := min(postCompactMaxTokenPerFile, budget-totalTokens)*postCompactBytesPerToken - wrapper
		if maxBytes <= 0 {
			break
		}
		if len(content) > maxBytes {
			content = content[:maxBytes]
		}

		msg := injectedUserMsg(fmt.Sprintf("<file-restore path=%q>\n%s\n</file-restore>", path, string(content)))
		totalTokens += agentctx.EstimateTokens(msg)
		out = append(out, msg)
	}
	return out
}

// shouldExcludeFromRestore returns true for files that are re-injected by
// other mechanisms (plan files, memory/config files) or that shouldn't be
// restored (binary, generated).
func shouldExcludeFromRestore(path string) bool {
	base := strings.ToLower(filepath.Base(path))

	// Instruction files — already in system block 2.
	switch base {
	case "claude.md", "agents.md", "agentctx.md":
		return true
	}
	// Plan files — re-injected via plan attachment
	if strings.HasSuffix(base, ".plan.md") {
		return true
	}
	// Memory / config directories. ToSlash first: on Windows these arrive as
	// backslash paths and the substring checks would never fire.
	slashed := filepath.ToSlash(path)
	if strings.Contains(slashed, "/memory/") || strings.Contains(slashed, "/.claude/") || strings.Contains(slashed, "/.codebot/") {
		return true
	}
	return false
}

// looksLikeBinary checks whether a file is binary by extension first, then by
// sampling up to 4096 bytes for null bytes or high non-printable ratio (>30%).
func looksLikeBinary(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".zip", ".tar", ".gz", ".exe", ".dll", ".so", ".class", ".jar", ".war",
		".7z", ".bin", ".dat", ".obj", ".o", ".a", ".lib", ".wasm", ".pyc", ".pyo",
		".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".webp",
		".mp3", ".mp4", ".avi", ".mov", ".wav", ".flac",
		".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp":
		return true
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false
	}

	nonPrintable := 0
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
		if buf[i] < 9 || (buf[i] > 13 && buf[i] < 32) {
			nonPrintable++
		}
	}
	return float64(nonPrintable)/float64(n) > 0.3
}

// collectReadFilesFromMessages scans kept messages for Read tool results to
// avoid re-reading files whose content is already in the retained context.
func collectReadFilesFromMessages(msgs []agentcore.AgentMessage) map[string]bool {
	result := make(map[string]bool)
	pending := map[string]string{} // tool_call_id → path

	for _, am := range msgs {
		msg, ok := am.(agentcore.Message)
		if !ok {
			continue
		}
		if msg.Role == agentcore.RoleAssistant {
			for _, tc := range msg.ToolCalls() {
				if tc.Name == "read" {
					if p := extractFilePath(tc.Args); p != "" {
						pending[tc.ID] = p
					}
				}
			}
		}
		if msg.Role == agentcore.RoleTool {
			callID, _ := msg.Metadata["tool_call_id"].(string)
			if path, ok := pending[callID]; ok {
				result[path] = true
			}
		}
	}
	return result
}

func extractFilePath(args []byte) string {
	// Reuse the same pattern as agentcore's extractPathArg
	type pathObj struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	var obj pathObj
	if json.Unmarshal(args, &obj) == nil {
		if obj.FilePath != "" {
			return obj.FilePath
		}
		return obj.Path
	}
	return ""
}
