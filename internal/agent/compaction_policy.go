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
)

var compactableTools = map[string]bool{
	"read":       true,
	"bash":       true,
	"grep":       true,
	"glob":       true,
	"ls":         true,
	"web_search": true,
	"web_fetch":  true,
}

func CodebotToolClassifier(name string) bool {
	if strings.HasPrefix(name, "mcp__") {
		return false
	}
	return compactableTools[name]
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

func (s *Session) handleAutomaticRewrite(info agentctx.RewriteEvent) {
	if !info.Changed {
		return
	}
	kind := compactionKindForStrategy(info.Strategy)
	s.recordCompactionAttempt(kind)
	s.emit(SessionEvent{
		Type:               SEAutoCompactionStart,
		CompactionReason:   info.Reason,
		CompactionKind:     kind,
		CompactionStrategy: info.Strategy,
	})
	s.recordCompactionResult(kind, true, info.TokensBefore, info.TokensAfter)
	compactedCount, keptCount, splitTurn := 0, 0, false
	if info.Info != nil {
		compactedCount = info.Info.CompactedCount
		keptCount = info.Info.KeptCount
		splitTurn = info.Info.IsSplitTurn
	}
	s.recordCompactionSnapshot(kind, info.Strategy, info.Reason, true, info.TokensBefore, info.TokensAfter, compactedCount, keptCount, splitTurn)
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
}

func compactionKindForStrategy(name string) CompactionKind {
	switch name {
	case "tool_result_microcompact":
		return CompactionKindMicro
	case "light_trim":
		return CompactionKindTrim
	default:
		return CompactionKindFull
	}
}

const (
	postCompactMaxFiles        = 5
	postCompactTokenBudget     = 50000
	postCompactMaxTokenPerFile = 5000
	postCompactBytesPerToken   = 4
)

func (s *Session) postCompactRecoveryMessages(_ context.Context, _ agentctx.SummaryInfo, _ []agentcore.AgentMessage) ([]agentcore.AgentMessage, error) {
	var out []agentcore.AgentMessage

	// 1. Skill reminder
	if reminder := s.invokedSkillReminderMessage(); reminder != nil {
		out = append(out, *reminder)
	}

	// 2. Deferred tools + static reminders
	s.mu.Lock()
	preamble := s.deferredToolsPreamble
	staticReminders := append([]string(nil), s.staticReminders...)
	s.mu.Unlock()

	if preamble != "" {
		out = append(out, injectedUserMsg(preamble))
	}
	for _, text := range staticReminders {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, injectedUserMsg(text))
	}

	return out, nil
}

// readRecentFiles re-reads files referenced by a committed summary rewrite so
// the model can continue editing without an extra Read call. Kept at the
// application layer; only used for explicit committed rewrites, not transient
// projected views.
//
// Strategy:
//   - modified files first (higher priority), then read-only
//   - skip files already in kept messages, plan files, memory files, binaries
//   - max 5 files, 50k token total budget, 5k per file
func readRecentFiles(info agentctx.SummaryInfo, kept []agentcore.AgentMessage) []agentcore.AgentMessage {
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
		if totalTokens >= postCompactTokenBudget {
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

		// Truncate to per-file budget
		maxBytes := postCompactMaxTokenPerFile * postCompactBytesPerToken
		if len(content) > maxBytes {
			content = content[:maxBytes]
		}
		// Truncate to remaining total budget
		remainBytes := (postCompactTokenBudget - totalTokens) * postCompactBytesPerToken
		if len(content) > remainBytes {
			content = content[:remainBytes]
		}

		tokens := (len(content) + postCompactBytesPerToken - 1) / postCompactBytesPerToken
		totalTokens += tokens

		text := fmt.Sprintf("<file-restore path=%q>\n%s\n</file-restore>", path, string(content))
		out = append(out, injectedUserMsg(text))
	}
	return out
}

// shouldExcludeFromRestore returns true for files that are re-injected by
// other mechanisms (plan files, memory/config files) or that shouldn't be
// restored (binary, generated).
func shouldExcludeFromRestore(path string) bool {
	base := strings.ToLower(filepath.Base(path))

	// Memory/config files — re-injected via static reminders
	switch base {
	case "claude.md", "agents.md", "agentctx.md":
		return true
	}
	// Plan files — re-injected via plan attachment
	if strings.HasSuffix(base, ".plan.md") {
		return true
	}
	// Memory directory files
	if strings.Contains(path, "/memory/") || strings.Contains(path, "/.claude/") || strings.Contains(path, "/.codebot/") {
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
