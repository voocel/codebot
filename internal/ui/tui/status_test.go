package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

func TestShimmerSweepsOverTheText(t *testing.T) {
	start := time.UnixMilli(0)
	seen := map[string]bool{}
	for ms := range 600 {
		s := shimmer("Running…", start.Add(time.Duration(ms)*time.Millisecond))
		if got := ansi.Strip(s); got != "Running…" {
			t.Fatalf("shimmer changed the text to %q", got)
		}
		seen[s] = true
	}
	if len(seen) < 10 {
		t.Errorf("the light took %d places in 600ms", len(seen))
	}
}

func TestTokensCountAsTheResponseStreams(t *testing.T) {
	var r run
	r.start(time.Now())
	delta := func(ev litellm.Event) { r.apply(agentcore.MessageDelta{Event: ev}, time.Now()) }

	r.apply(agentcore.MessageStart{}, time.Now())
	delta(litellm.UsageEvent{Usage: litellm.Usage{InputTokens: 1200, OutputTokens: 1}})
	if in, out := r.tokens(); in != 1200 || out != 1 {
		t.Errorf("at the start: ↑%d ↓%d", in, out)
	}
	delta(litellm.TextDelta{Text: strings.Repeat("word ", 80)})
	if _, out := r.tokens(); out != 100 {
		t.Errorf("400 bytes streamed: ↓%d, want about 100", out)
	}
	end := agentcore.Message{Role: litellm.RoleAssistant, Usage: &agentcore.Usage{Usage: litellm.Usage{InputTokens: 1200, OutputTokens: 96}}}
	r.apply(agentcore.MessageEnd{Message: end}, time.Now())
	if in, out := r.tokens(); in != 1200 || out != 96 {
		t.Errorf("at the end: ↑%d ↓%d, want what the provider counted", in, out)
	}

	// The next response adds to them.
	r.apply(agentcore.MessageStart{}, time.Now())
	delta(litellm.UsageEvent{Usage: litellm.Usage{InputTokens: 1400}})
	if in, _ := r.tokens(); in != 2600 {
		t.Errorf("the second response: ↑%d", in)
	}
}

func TestShownTokensRoll(t *testing.T) {
	start := time.Now()
	var r run
	r.start(start)
	r.in = 3000
	prev := 0.0
	for f := 1; f <= 90; f++ {
		r.roll(start.Add(time.Duration(f) * time.Second / 30))
		if r.shown[0] < prev {
			t.Fatalf("frame %d: the count went back from %v to %v", f, prev, r.shown[0])
		}
		if f == 1 && (r.shown[0] < 200 || r.shown[0] > 300) {
			t.Errorf("the first frame closed %v of 3000, want about a twelfth", r.shown[0])
		}
		prev = r.shown[0]
	}
	if r.shown[0] != 3000 {
		t.Errorf("after 3s the count shows %v", r.shown[0])
	}
}
