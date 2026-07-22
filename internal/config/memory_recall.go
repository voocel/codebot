package config

// Cheap, LLM-free recall over the auto-memory directory: scan topic-file
// frontmatter, score it lexically against the user's message, and surface
// the few files that clearly relate. Same shape as CC's findRelevantMemories
// but with word/bigram overlap instead of a model-based selector, so recall
// costs no tokens and can run synchronously on every prompt.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	recallMaxScanFiles = 200  // hard cap on files considered per scan
	recallHeaderLines  = 30   // frontmatter must close within this many lines
	recallMaxFileBytes = 4096 // per-file injection cap; full file stays a Read away
	recallMinMsgRunes  = 8    // shorter messages carry too little signal to match
	recallMinScore     = 3    // one name hit, or several description hits
)

// MemoryRecall is one memory file selected for injection.
type MemoryRecall struct {
	Path      string
	Content   string // truncated to recallMaxFileBytes
	Age       time.Duration
	Truncated bool
}

// RecallMemories scans dir for topic files whose frontmatter matches the
// message and returns at most maxFiles of them, best match first. Files in
// exclude (already surfaced this session) and MEMORY.md (always injected
// separately) are skipped. Returns nil when the message is too short to
// score meaningfully.
func RecallMemories(dir, message string, exclude map[string]bool, maxFiles int) []MemoryRecall {
	if dir == "" || maxFiles <= 0 {
		return nil
	}
	if utf8.RuneCountInString(strings.TrimSpace(message)) < recallMinMsgRunes {
		return nil
	}
	msgWords, msgBigrams := recallTerms(message, 3)
	if len(msgWords) == 0 && len(msgBigrams) == 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	type candidate struct {
		rec   MemoryRecall
		score int
		mtime time.Time
	}
	var cands []candidate
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") || strings.EqualFold(e.Name(), "MEMORY.md") {
			continue
		}
		if scanned >= recallMaxScanFiles {
			break
		}
		scanned++
		path := filepath.Join(dir, e.Name())
		if exclude[path] {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		name, desc := parseMemoryHeader(content, e.Name())
		score := recallScore(name, desc, msgWords, msgBigrams)
		if score < recallMinScore {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		body, truncated := truncateUTF8(content, recallMaxFileBytes)
		cands = append(cands, candidate{
			rec:   MemoryRecall{Path: path, Content: body, Age: time.Since(info.ModTime()), Truncated: truncated},
			score: score,
			mtime: info.ModTime(),
		})
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].mtime.After(cands[j].mtime)
	})
	if len(cands) > maxFiles {
		cands = cands[:maxFiles]
	}
	out := make([]MemoryRecall, len(cands))
	for i, c := range cands {
		out[i] = c.rec
	}
	return out
}

// FormatMemoryRecallReminder wraps a recalled memory in a system-reminder
// with its age; memories older than a day carry a staleness caveat.
func FormatMemoryRecallReminder(r MemoryRecall) string {
	body := r.Content
	if r.Truncated {
		body += "\n\n<!-- truncated — Read the file for the full content -->"
	}
	note := ""
	if r.Age > 24*time.Hour {
		note = "\n\nThis memory is a point-in-time observation — verify that the files, functions, or flags it mentions still exist before relying on it."
	}
	return "<system-reminder>\nMemory recall (auto-memory, saved " + memoryAgeText(r.Age) + "): " + r.Path + "\n\n" + body + note + "\n</system-reminder>"
}

func memoryAgeText(age time.Duration) string {
	days := int(age.Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", days)
	}
}

// parseMemoryHeader extracts name and description from a topic file's
// frontmatter. Files without frontmatter (written before the format existed)
// fall back to the filename stem and the first content line, so they stay
// recallable until dream migrates them.
func parseMemoryHeader(content, filename string) (name, description string) {
	name = strings.TrimSuffix(filename, filepath.Ext(filename))
	lines := strings.Split(content, "\n")
	body := lines
	if strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines) && i <= recallHeaderLines; i++ {
			line := strings.TrimSpace(lines[i])
			if line == "---" {
				body = lines[i+1:]
				break
			}
			if v, ok := strings.CutPrefix(line, "name:"); ok {
				if v = strings.TrimSpace(v); v != "" {
					name = v
				}
			} else if v, ok := strings.CutPrefix(line, "description:"); ok {
				description = strings.TrimSpace(v)
			}
		}
	}
	if description == "" {
		for _, line := range body {
			if line = strings.TrimSpace(strings.TrimLeft(line, "# ")); line != "" {
				description = line
				break
			}
		}
	}
	return name, description
}

// recallScore rates a memory header against the message terms: name hits
// weigh 3 (a name token is a deliberate identifier), description hits weigh
// 1. Each distinct term counts once.
func recallScore(name, desc string, msgWords, msgBigrams map[string]bool) int {
	score := 0
	nameWords, nameBigrams := recallTerms(name, 3)
	for w := range nameWords {
		if msgWords[w] {
			score += 3
		}
	}
	for b := range nameBigrams {
		if msgBigrams[b] {
			score += 3
		}
	}
	descWords, descBigrams := recallTerms(desc, 4)
	for w := range descWords {
		if !nameWords[w] && msgWords[w] {
			score++
		}
	}
	for b := range descBigrams {
		if !nameBigrams[b] && msgBigrams[b] {
			score++
		}
	}
	return score
}

// recallStopwords are common English words too generic to signal relevance.
var recallStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"that": true, "this": true, "when": true, "what": true, "have": true,
	"must": true, "should": true, "into": true, "not": true, "are": true,
	"use": true, "used": true, "using": true, "user": true, "file": true,
}

// recallTerms tokenizes text into lowercase ASCII words (length >= minWord,
// stopwords removed) and CJK bigrams — the bigrams make matching work for
// Chinese, which has no word boundaries to split on.
func recallTerms(text string, minWord int) (words, bigrams map[string]bool) {
	words = make(map[string]bool)
	bigrams = make(map[string]bool)
	var word []rune
	var prevCJK rune
	flush := func() {
		if len(word) >= minWord {
			w := strings.ToLower(string(word))
			if !recallStopwords[w] {
				words[w] = true
			}
		}
		word = word[:0]
	}
	for _, r := range text {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			word = append(word, r)
			prevCJK = 0
		case unicode.Is(unicode.Han, r):
			flush()
			if prevCJK != 0 {
				bigrams[string([]rune{prevCJK, r})] = true
			}
			prevCJK = r
		default:
			flush()
			prevCJK = 0
		}
	}
	flush()
	return words, bigrams
}

// truncateUTF8 cuts s at the last rune boundary within n bytes.
func truncateUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}
