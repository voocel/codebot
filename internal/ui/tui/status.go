package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// run is what the status line shows of the run under way.
type run struct {
	active  bool
	started time.Time
	tools   int

	// in and out are the tokens of the responses that ended; reply is the
	// response under way, as far as it went.
	in, out int
	reply   reply
	// shown is what the status line shows of the tokens, rolling toward
	// them since rolled.
	shown  [2]float64
	rolled time.Time

	retry       int // the attempt coming, 0 when none
	retryAt     time.Time
	compactFrom time.Time // when the compaction under way started, zero when none

	todos []todo.Item
}

// reply is a response's tokens as it streams: what its provider reported,
// and the bytes that streamed, which tell its output before the provider
// does at the end.
type reply struct {
	in, out  int
	streamed int
}

// bytesPerToken estimates tokens from text: about four characters of
// English, or one or two of CJK, three bytes each.
const bytesPerToken = 4

// tokens returns the tokens the run spent, the response under way
// included: its input once the provider reports it, its output estimated
// from what streamed until then.
func (r *run) tokens() (in, out int) {
	return r.in + r.reply.in, r.out + max(r.reply.out, r.reply.streamed/bytesPerToken)
}

// rollTime is how fast the tokens shown catch up: a thirtieth of a second
// closes a twelfth of the gap.
const rollTime = 0.38 // seconds

// roll moves the tokens shown toward the tokens, the further the faster, so
// that they run rather than jump.
func (r *run) roll(now time.Time) {
	k := 1 - math.Exp(-now.Sub(r.rolled).Seconds()/rollTime)
	r.rolled = now
	in, out := r.tokens()
	r.shown = [2]float64{approach(r.shown[0], in, k), approach(r.shown[1], out, k)}
}

func approach(shown float64, target int, k float64) float64 {
	t := float64(target)
	if shown >= t {
		return t
	}
	return min(shown+max((t-shown)*k, 1), t)
}

// compacting reports whether a compaction is under way.
func (r *run) compacting() bool { return !r.compactFrom.IsZero() }

func (r *run) start(now time.Time) {
	*r = run{active: true, started: now, rolled: now, todos: r.todos}
}

// apply takes an event of the run.
func (r *run) apply(ev agentcore.Event, now time.Time) {
	switch e := ev.(type) {
	case agentcore.MessageStart:
		r.retry = 0
		r.reply = reply{}
	case agentcore.MessageDelta:
		switch d := e.Event.(type) {
		case litellm.TextDelta:
			r.reply.streamed += len(d.Text)
		case litellm.ReasoningDelta:
			r.reply.streamed += len(d.Text)
		case litellm.ToolUseDelta:
			r.reply.streamed += len(d.Arguments)
		case litellm.UsageEvent:
			r.reply.in, r.reply.out = d.Usage.InputTokens, d.Usage.OutputTokens
		}
	case agentcore.MessageEnd:
		if e.Message.Role == litellm.RoleAssistant {
			if u := e.Message.Usage; u != nil {
				r.in += u.InputTokens
				r.out += u.OutputTokens
			} else {
				// A provider that never told keeps the estimate.
				r.in, r.out = r.tokens()
			}
			r.reply = reply{}
		}
	case agentcore.ToolStart:
		r.tools++
	case agentcore.ToolEnd:
		if e.Call.Name == todo.ToolName && !e.Result.IsError {
			if items, err := todo.Parse(e.Call.Args); err == nil {
				r.todos = items
			}
		}
	case agentcore.Retry:
		r.retry, r.retryAt = e.Attempt, now.Add(e.Delay)
	case agentcore.CompactionStart:
		r.compactFrom = now
	case agentcore.CompactionEnd:
		r.compactFrom = time.Time{}
	}
}

// summary sums up a run that ended, "" for one too short to.
func (r *run) summary(now time.Time) string {
	d := now.Sub(r.started)
	if r.tools == 0 && d < 10*time.Second {
		return ""
	}
	parts := []string{"Worked for " + transcript.Duration(d)}
	if r.tools > 0 {
		parts = append(parts, fmt.Sprintf("%d tool %s", r.tools, plural(r.tools, "call")))
	}
	if r.in+r.out > 0 {
		parts = append(parts, "↑"+transcript.Tokens(r.in)+" ↓"+transcript.Tokens(r.out))
	}
	return "✻ " + strings.Join(parts, " · ")
}

// lines renders the status above the input: what the run is doing, the
// todo list, and the inputs waiting to join the conversation.
func (r *run) lines(width int, now time.Time, pending []pending) []string {
	var out []string
	if r.active || r.compacting() {
		// A compaction runs within a run, or on its own.
		label, since := "Running…", r.started
		switch {
		case r.compacting():
			label, since = "Compacting the conversation", r.compactFrom
		case r.retry > 0:
			label = fmt.Sprintf("Retrying in %s (attempt %d)", transcript.Duration(max(r.retryAt.Sub(now), 0)), r.retry)
		}
		line := twinkle(now) + " " + shimmer(label, now) + theme.SubtleText.Render(" · "+transcript.Duration(now.Sub(since)))
		if r.active {
			if in, out := int(r.shown[0]), int(r.shown[1]); in+out > 0 {
				line += theme.SubtleText.Render(" · ↑" + transcript.Tokens(in) + " ↓" + transcript.Tokens(out))
			}
			line += theme.FaintText.Render(" · esc to stop")
		}
		out = append(out, line)
	}
	out = append(out, todoLines(r.todos)...)
	for _, p := range pending {
		out = append(out, theme.SubtleText.Render("↳ ")+theme.MutedText.Render(strings.ReplaceAll(p.text, "\n", " ")))
	}
	for i, l := range out {
		out[i] = ansi.Truncate(" "+l, width, "…")
	}
	return out
}

// shellLine shows the "!" line running; esc stops it once no run is
// under way to stop first.
func shellLine(s *shellRun, width int, now time.Time, stoppable bool) string {
	line := twinkle(now) + " " + shimmer("Running", now) + " " + theme.Text.Render(ansi.Truncate(s.line, max(width/2, 10), "…")) +
		theme.SubtleText.Render(" · "+transcript.Duration(now.Sub(s.started)))
	if stoppable {
		line += theme.FaintText.Render(" · esc to stop")
	}
	return ansi.Truncate(" "+line, width, "…")
}

// star is the status line's spinner, a frame every 33ms.
var star = []string{"·", "✢", "✶", "✽", "✶", "✢", "·"}

func twinkle(now time.Time) string {
	return lipgloss.NewStyle().Foreground(theme.Live).Render(star[now.UnixMilli()/33%int64(len(star))])
}

// shimmer renders text with a light sweeping over it, 20 characters a
// second: 2 characters at full brightness, fading over 2 on either side,
// then a pause as long as the light is wide.
func shimmer(text string, now time.Time) string {
	const speed, band, slope = 20.0, 2.0, 2.0
	runes := []rune(text)
	center := math.Mod(float64(now.UnixMilli())/1000*speed, float64(len(runes))+band+2*slope)
	var b strings.Builder
	for i, r := range runes {
		var bright float64
		switch d := math.Abs(float64(i) - center); {
		case d <= band/2:
			bright = 1
		case d < band/2+slope:
			bright = 1 - (d-band/2)/slope
		}
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Glint[int(bright*15)]).Render(string(r)))
	}
	return b.String()
}

const maxTodos = 6

// todoLines renders the todo list while any of it is left to do.
func todoLines(items []todo.Item) []string {
	pending, active, done := todo.Counts(items)
	if pending+active == 0 {
		return nil
	}
	out := []string{theme.MutedText.Render(fmt.Sprintf("Todos %d/%d", done, len(items)))}
	shown := items
	if len(shown) > maxTodos {
		// Keep the work in progress in view.
		start := 0
		for i, it := range items {
			if it.Status != todo.Completed {
				start = max(i-1, 0)
				break
			}
		}
		shown = items[min(start, len(items)-maxTodos):][:maxTodos]
	}
	for _, it := range shown {
		switch it.Status {
		case todo.Completed:
			out = append(out, theme.FaintText.Strikethrough(true).Render("  ✓ "+it.Content))
		case todo.InProgress:
			out = append(out, theme.WarmText.Render("  ◐ "+it.Content))
		default:
			out = append(out, theme.MutedText.Render("  ○ "+it.Content))
		}
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
