package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeMemoryFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseMemoryHeader(t *testing.T) {
	t.Parallel()

	name, desc := parseMemoryHeader("---\nname: windows-tests\ndescription: HOME isolation failures on Windows\ntype: project\n---\n\nbody", "file.md")
	if name != "windows-tests" || desc != "HOME isolation failures on Windows" {
		t.Fatalf("frontmatter parse = %q / %q", name, desc)
	}

	// No frontmatter: stem + first content line.
	name, desc = parseMemoryHeader("# Docker compose tips\n\ndetails here", "docker-compose-tips.md")
	if name != "docker-compose-tips" || desc != "Docker compose tips" {
		t.Fatalf("fallback parse = %q / %q", name, desc)
	}

	// Frontmatter without description: fall back to body AFTER the header,
	// not to a frontmatter line.
	name, desc = parseMemoryHeader("---\nname: slug\n---\n\n# First heading: with colon", "x.md")
	if name != "slug" || desc != "First heading: with colon" {
		t.Fatalf("partial frontmatter = %q / %q", name, desc)
	}
}

func TestRecallTerms(t *testing.T) {
	t.Parallel()

	words, bigrams := recallTerms("Use the Windows API 修复表驱动测试", 3)
	for _, w := range []string{"windows", "api"} {
		if !words[w] {
			t.Errorf("missing word %q", w)
		}
	}
	if words["the"] || words["use"] {
		t.Error("stopwords must be dropped")
	}
	for _, b := range []string{"修复", "表驱", "驱动", "测试"} {
		if !bigrams[b] {
			t.Errorf("missing bigram %q", b)
		}
	}
}

func TestRecallMemories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	writeMemoryFile(t, dir, "MEMORY.md", "windows test failures index entry")
	matching := writeMemoryFile(t, dir, "win.md",
		"---\nname: windows-test-failures\ndescription: HOME isolation breaks os.UserHomeDir on Windows\ntype: project\n---\n\ndetails")
	writeMemoryFile(t, dir, "other.md",
		"---\nname: api-conventions\ndescription: REST endpoint naming rules\ntype: project\n---\n\ndetails")

	msg := "why do the windows test failures happen under HOME isolation"
	got := RecallMemories(dir, msg, nil, 3)
	if len(got) != 1 {
		t.Fatalf("recalls = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].Path != matching {
		t.Fatalf("recalled %q, want %q", got[0].Path, matching)
	}

	// MEMORY.md must never be recalled even though it matches.
	for _, r := range got {
		if strings.HasSuffix(r.Path, "MEMORY.md") {
			t.Fatal("MEMORY.md must be excluded from recall")
		}
	}

	// Excluded (already surfaced) files are skipped.
	if got := RecallMemories(dir, msg, map[string]bool{matching: true}, 3); len(got) != 0 {
		t.Fatalf("excluded file still recalled: %+v", got)
	}

	// Short messages carry no signal.
	if got := RecallMemories(dir, "hi", nil, 3); got != nil {
		t.Fatalf("short message must not recall, got %+v", got)
	}
}

func TestRecallMemoriesCJK(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeMemoryFile(t, dir, "tests.md",
		"---\nname: test-style\ndescription: 用户偏好表驱动测试而非重复用例\ntype: feedback\n---\n\ndetails")

	got := RecallMemories(dir, "帮我把这个函数改成表驱动测试的写法", nil, 3)
	if len(got) != 1 {
		t.Fatalf("CJK recall = %d files, want 1", len(got))
	}
}

func TestRecallMemoriesLegacyFileWithoutFrontmatter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeMemoryFile(t, dir, "docker-compose-tips.md", "# Docker compose 使用技巧\n\ndetails")

	got := RecallMemories(dir, "how to fix docker compose networking here", nil, 3)
	if len(got) != 1 {
		t.Fatalf("legacy recall = %d files, want 1", len(got))
	}
}

func TestRecallMemoriesTruncatesAndCaps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	long := "---\nname: windows-notes\ndescription: windows notes\n---\n\n" + strings.Repeat("填充内容 padding line\n", 400)
	writeMemoryFile(t, dir, "big.md", long)
	for i := range 4 {
		writeMemoryFile(t, dir, "w"+string(rune('a'+i))+".md",
			"---\nname: windows-extra\ndescription: windows notes\n---\nbody")
	}

	got := RecallMemories(dir, "tell me about the windows notes collection", nil, 3)
	if len(got) != 3 {
		t.Fatalf("maxFiles cap: got %d, want 3", len(got))
	}
	for _, r := range got {
		if strings.HasSuffix(r.Path, "big.md") {
			if !r.Truncated || len(r.Content) > recallMaxFileBytes {
				t.Fatalf("big file not truncated: %d bytes, truncated=%v", len(r.Content), r.Truncated)
			}
		}
	}
}

func TestFormatMemoryRecallReminder(t *testing.T) {
	t.Parallel()

	fresh := FormatMemoryRecallReminder(MemoryRecall{Path: "p.md", Content: "c", Age: time.Hour})
	if !strings.Contains(fresh, "<system-reminder>") || !strings.Contains(fresh, "saved today") {
		t.Fatalf("fresh reminder malformed:\n%s", fresh)
	}
	if strings.Contains(fresh, "point-in-time") {
		t.Fatal("fresh memory must not carry the staleness caveat")
	}

	stale := FormatMemoryRecallReminder(MemoryRecall{Path: "p.md", Content: "c", Age: 72 * time.Hour, Truncated: true})
	if !strings.Contains(stale, "saved 3 days ago") || !strings.Contains(stale, "point-in-time") {
		t.Fatalf("stale reminder missing age/caveat:\n%s", stale)
	}
	if !strings.Contains(stale, "truncated") {
		t.Fatal("truncated note missing")
	}
}
