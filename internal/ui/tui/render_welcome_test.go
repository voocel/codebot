package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestFrameCardNeverBreaks locks the invariant that an over-wide body line is
// truncated instead of pushing the right border out.
func TestFrameCardNeverBreaks(t *testing.T) {
	long := strings.Repeat("x", 200)
	card := frameCard("tag", []string{"short", long}, 60)
	var want int
	for _, line := range strings.Split(card, "\n") {
		w := lipgloss.Width(line)
		if want == 0 {
			want = w
			continue
		}
		if w != want {
			t.Fatalf("frame line width %d != %d:\n%s", w, want, card)
		}
	}
}

func TestPairClamp(t *testing.T) {
	// Both fit: untouched.
	a, b := pairClamp("openai", "gpt-5", 40)
	if a != "openai" || b != "gpt-5" {
		t.Fatalf("fitting pair must pass through, got %q %q", a, b)
	}
	// Long path + short branch: branch keeps its size, path takes the rest,
	// and the joined line fits the budget.
	path := "E:\\very\\deep\\workspace\\monorepo\\services\\billing\\my-project"
	a, b = pairClamp(path, "branch main", 40)
	if b != "branch main" {
		t.Fatalf("short chip must keep its natural size, got %q", b)
	}
	if got := len([]rune(a)) + 3 + len([]rune(b)); got > 40 {
		t.Fatalf("joined width %d exceeds budget 40", got)
	}
	if !strings.HasSuffix(a, "my-project") {
		t.Fatalf("path tail must survive: %q", a)
	}
	// Both long: each ends up around half, jointly within budget.
	a, b = pairClamp(strings.Repeat("a", 60), strings.Repeat("b", 60), 40)
	if got := len([]rune(a)) + 3 + len([]rune(b)); got > 40 {
		t.Fatalf("joined width %d exceeds budget 40", got)
	}
}

func TestMiddleClamp(t *testing.T) {
	if got := middleClamp("short", 10); got != "short" {
		t.Fatalf("fitting text must pass through, got %q", got)
	}
	got := middleClamp("E:\\very\\deep\\nested\\path\\to\\my-project", 20)
	if len([]rune(got)) != 20 {
		t.Fatalf("clamped length = %d, want 20 (%q)", len([]rune(got)), got)
	}
	if !strings.HasPrefix(got, "E:\\") || !strings.HasSuffix(got, "my-project") || !strings.Contains(got, "…") {
		t.Fatalf("middle clamp must keep both ends: %q", got)
	}
}

// TestRenderWelcomeExtremeChipsKeepFrame pushes a single chip past the whole
// line budget: the frame must hold and the path tail must stay readable.
func TestRenderWelcomeExtremeChipsKeepFrame(t *testing.T) {
	m := &Model{}
	m.Width = 78
	m.Version = "dev"
	m.Provider = "openrouter"
	m.ModelName = "some-vendor/an-extremely-long-experimental-model-identifier-v2-preview-0631"
	m.ContextWindow = 1000000
	m.Cwd = "E:\\very\\deep\\workspace\\monorepo\\services\\billing\\internal\\adapters\\my-project"
	m.GitBranch = "feature/very-long-branch-name-for-testing"

	view := m.renderWelcome()
	var cardWidth int
	for _, line := range strings.Split(view, "\n") {
		if !strings.ContainsAny(line, "╭│╰") {
			continue
		}
		w := lipgloss.Width(line)
		if cardWidth == 0 {
			cardWidth = w
			continue
		}
		if w != cardWidth {
			t.Fatalf("welcome frame line width %d != %d\nline: %q", w, cardWidth, line)
		}
	}
	if !strings.Contains(view, "my-project") {
		t.Fatal("path tail must survive the middle clamp")
	}
	if !strings.Contains(view, "…") {
		t.Fatal("over-wide chips should carry a middle ellipsis")
	}
}

// TestRenderWelcomeLongChipsKeepFrame reproduces the reported overflow: a long
// model name plus a long cwd must wrap inside the card, not break the border.
func TestRenderWelcomeLongChipsKeepFrame(t *testing.T) {
	m := &Model{}
	m.Width = 78
	m.Version = "dev"
	m.Provider = "deepseek"
	m.ModelName = "deepseek-v4-flash"
	m.ReasoningEffort = "high"
	m.ContextWindow = 128000
	m.Cwd = "E:\\project\\me\\codebot"
	m.GitBranch = "main"

	view := m.renderWelcome()
	var cardWidth int
	for _, line := range strings.Split(view, "\n") {
		if !strings.ContainsAny(line, "╭│╰") {
			continue
		}
		w := lipgloss.Width(line)
		if cardWidth == 0 {
			cardWidth = w
			continue
		}
		if w != cardWidth {
			t.Fatalf("welcome frame line width %d != %d\nline: %q", w, cardWidth, line)
		}
	}
	if cardWidth == 0 {
		t.Fatal("no framed lines found")
	}

	// Fixed layout: provider · model on one line, path · branch on the next.
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "(128k)") && strings.Contains(line, "E:\\") {
			t.Fatalf("model and path must sit on separate lines: %q", line)
		}
	}
	if !strings.Contains(view, "thinking high") {
		t.Fatal("reasoning effort chip missing from the model line")
	}
}

// TestRenderWelcomeThinkingChipVisibility locks the display convention:
// "auto" (provider default on a reasoning model) shows, "off" and "" hide.
func TestRenderWelcomeThinkingChipVisibility(t *testing.T) {
	for effort, want := range map[string]bool{"auto": true, "high": true, "off": false, "": false} {
		m := &Model{}
		m.Width = 78
		m.Provider = "deepseek"
		m.ModelName = "deepseek-v4-flash"
		m.ReasoningEffort = effort
		if got := strings.Contains(m.renderWelcome(), "thinking"); got != want {
			t.Fatalf("effort %q: thinking chip shown = %v, want %v", effort, got, want)
		}
	}
}
