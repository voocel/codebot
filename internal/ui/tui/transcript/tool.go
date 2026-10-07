package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

type ToolState int

const (
	Running ToolState = iota
	Succeeded
	Failed
	// Interrupted means the run ended before the call did.
	Interrupted
)

type Tool struct {
	rev
	ID      string
	Name    string
	Args    json.RawMessage
	Preview string // what the tool's Check previewed, such as an edit's diff
	State   ToolState
	Result  string
	Started time.Time

	args    args
	output  []string // progress lines of a running tool
	agents  []*Agent // runs of a subagent call
	toggled bool
}

type Agent struct {
	ID         string // "explore#2"
	Name       string // "explore"
	Transcript *Transcript
	Turns      int
	Tools      int
	In, Out    int
	Done       bool
}

func newTool(id, name string, raw json.RawMessage, preview string, started time.Time) *Tool {
	t := &Tool{ID: id, Name: name, Args: raw, Preview: preview, Started: started}
	_ = json.Unmarshal(raw, &t.args)
	return t
}

func (t *Tool) Toggle() { t.toggled = !t.toggled; t.bump() }

func (t *Tool) Live() bool { return t.State == Running }

func (t *Tool) Agents() []*Agent { return t.agents }

func (t *Tool) finish(result string, failed bool) {
	t.Result = strings.TrimRight(result, "\n")
	t.State = Succeeded
	if failed {
		t.State = Failed
	}
	t.bump()
}

func (t *Tool) interrupt() {
	if t.State == Running {
		t.State = Interrupted
		t.bump()
	}
	for _, a := range t.agents {
		a.Transcript.interrupt()
	}
}

func (t *Tool) progress(p any) {
	switch p := p.(type) {
	case string: // a line of bash output
		t.output = append(t.output, strings.TrimRight(p, "\n"))
	case subagent.Progress:
		t.agent(p.Spawn).apply(p.Event)
	default:
		return
	}
	t.bump()
}

func (t *Tool) agent(s subagent.Spawn) *Agent {
	for _, a := range t.agents {
		if a.ID == s.ID {
			return a
		}
	}
	a := &Agent{ID: s.ID, Name: s.Agent, Transcript: New()}
	t.agents = append(t.agents, a)
	return a
}

func (a *Agent) apply(ev agentcore.Event) {
	a.Transcript.Apply(ev)
	switch e := ev.(type) {
	case agentcore.MessageEnd:
		if u := e.Message.Usage; u != nil && e.Message.Role == litellm.RoleAssistant {
			a.In += u.InputTokens
			a.Out += u.OutputTokens
		}
	case agentcore.ToolStart:
		a.Tools++
	case agentcore.RunEnd:
		a.Turns, a.Done = e.Turns, true
	}
}

func (t *Tool) Render(p Params) []string {
	v := viewOf(t.Name)
	name, arg := v.name(t), v.arg(t.args)
	head := t.icon(p.Now) + " " + theme.Bold.Render(name)
	if arg != "" {
		head += theme.MutedText.Render("(" + arg + ")")
	}
	var suffix string
	if t.State == Running {
		if d := p.Now.Sub(t.Started); d >= 2*time.Second {
			suffix = theme.SubtleText.Render(" · " + Duration(d))
		}
	}
	lines := []string{fit(head, p.Width-ansi.StringWidth(suffix)) + suffix}

	expanded := p.Expanded != t.toggled
	var b []string
	switch t.State {
	case Failed:
		b = errorBody(t.Result, p.Width, expanded)
	case Interrupted:
		b = []string{theme.SubtleText.Render("Interrupted")}
	default:
		b = v.body(t, p.Width, expanded)
	}
	return append(lines, body(b, p.Width)...)
}

func (t *Tool) icon(now time.Time) string {
	switch t.State {
	case Running:
		return theme.WarmText.Render(Spinner(now))
	case Failed:
		return theme.ErrorText.Render(bullet)
	case Interrupted:
		return theme.SubtleText.Render(bullet)
	default:
		return theme.OKText.Render(bullet)
	}
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner advances a frame every 80ms.
func Spinner(now time.Time) string { return spinner[now.UnixMilli()/80%int64(len(spinner))] }

// Duration formats d as "8s", "1m 20s" or "2h 5m".
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// Ago returns "just now", "5m ago", "3h ago", "yesterday" or "4d ago".
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Tokens formats a count as "950", "12.3k" or "1.2M".
func Tokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
}
