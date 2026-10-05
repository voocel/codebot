// Package onboarding is the first-run setup: it asks for a provider, a
// model and an API key, and saves them with config.ApplySetup before the
// App boots.
package onboarding

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	llmprovider "github.com/voocel/litellm/provider"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Result is what the setup saved; Saved is false when the user left it.
type Result struct {
	Saved    bool
	Provider string
	Model    string
	Path     string
}

// Run asks for the setup until the user saves it or leaves.
func Run() (Result, error) {
	theme.Detect()
	final, err := tea.NewProgram(newWizard()).Run()
	if err != nil {
		return Result{}, fmt.Errorf("run setup: %w", err)
	}
	return final.(*wizard).result, nil
}

type step int

const (
	pickProvider step = iota
	describeCustom
	enterModel
	enterKey
)

// The fields of a custom provider.
const (
	fieldName = iota
	fieldProtocol
	fieldURL
)

type provider struct{ key, name string }

var providers = []provider{
	{"openai", "OpenAI"},
	{"anthropic", "Anthropic"},
	{"gemini", "Google Gemini"},
	{"openrouter", "OpenRouter"},
	{"deepseek", "DeepSeek"},
}

// custom is the row after the providers.
var custom = len(providers)

type wizard struct {
	width int
	step  step
	row   int // the provider picked, custom past the list
	field int // the custom field in focus

	name, url, model, key textinput.Model
	protocols             []string
	protocol              int

	modelFor string // the provider the model and key were typed for
	err      string
	done     bool
	result   Result
}

func newWizard() *wizard {
	// Bedrock signs in with AWS keys, not an API key.
	protocols := slices.DeleteFunc(llmprovider.Names(), func(t string) bool { return t == "bedrock" })
	return &wizard{
		name:      input("my-provider", false),
		url:       input("optional · the protocol's default endpoint", false),
		model:     input("the exact model id", false),
		key:       input("paste it here", true),
		protocols: protocols,
		protocol:  max(slices.Index(protocols, "openai"), 0),
	}
}

func input(placeholder string, secret bool) textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.SetVirtualCursor(false)
	if secret {
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
	}
	return in
}

func (w *wizard) Init() tea.Cmd { return nil }

func (w *wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		for _, in := range w.inputs() {
			in.SetWidth(max(w.width-labelWidth-3, 8))
		}
		return w, nil
	case tea.KeyPressMsg:
		k := msg.String()
		if k == "ctrl+c" {
			return w, w.quit()
		}
		if cmd, ok := w.press(k); ok {
			return w, cmd
		}
	}
	if in := w.focused(); in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		return w, cmd
	}
	return w, nil
}

// press handles the keys of the step; ok is false for keys the focused field
// takes.
func (w *wizard) press(k string) (cmd tea.Cmd, ok bool) {
	switch w.step {
	case pickProvider:
		switch k {
		case "up", "k":
			w.row = max(w.row-1, 0)
		case "down", "j":
			w.row = min(w.row+1, custom)
		case "enter":
			w.pick()
		case "esc":
			return w.quit(), true
		default:
			if len(k) == 1 && k[0] >= '1' && int(k[0]-'1') <= custom {
				w.row = int(k[0] - '1')
				w.pick()
			}
		}
		return nil, true

	case describeCustom:
		switch k {
		case "esc":
			w.goTo(pickProvider)
		case "up", "shift+tab":
			w.field = max(w.field-1, fieldName)
		case "down", "tab":
			w.field = min(w.field+1, fieldURL)
		case "left", "right":
			if w.field != fieldProtocol {
				return nil, false
			}
			n := len(w.protocols)
			if k == "left" {
				w.protocol = (w.protocol + n - 1) % n
			} else {
				w.protocol = (w.protocol + 1) % n
			}
		case "enter":
			switch {
			case w.field < fieldURL:
				w.field++
			case token(w.name.Value()) == "":
				w.err, w.field = "A name is required", fieldName
			default:
				w.goTo(enterModel)
			}
		default:
			return nil, false
		}
		w.focus()
		return nil, true

	case enterModel:
		switch k {
		case "esc":
			if w.row == custom {
				w.goTo(describeCustom)
			} else {
				w.goTo(pickProvider)
			}
		case "enter":
			if token(w.model.Value()) == "" {
				w.err = "A model id is required"
			} else {
				w.goTo(enterKey)
			}
		default:
			return nil, false
		}
		return nil, true

	default:
		switch k {
		case "esc":
			w.goTo(enterModel)
		case "enter":
			return w.save(), true
		default:
			return nil, false
		}
		return nil, true
	}
}

// pick moves on from the provider picked. The model typed for a provider
// stays while the provider does; it is never filled in, as defaults go
// stale.
func (w *wizard) pick() {
	if w.row == custom {
		w.goTo(describeCustom)
		return
	}
	w.goTo(enterModel)
}

func (w *wizard) goTo(s step) {
	w.step, w.err = s, ""
	if s == enterModel {
		// What was typed belongs to the provider it was typed for.
		if id := w.identity(); id != w.modelFor {
			w.model.Reset()
			w.key.Reset()
			w.modelFor = id
		}
	}
	w.focus()
}

// identity names the provider the model is for.
func (w *wizard) identity() string {
	if w.row == custom {
		return "custom/" + w.protocols[w.protocol] + "/" + token(w.name.Value())
	}
	return providers[w.row].key
}

// focused returns the field that takes the keys, nil when none does.
func (w *wizard) focused() *textinput.Model {
	switch w.step {
	case describeCustom:
		switch w.field {
		case fieldName:
			return &w.name
		case fieldURL:
			return &w.url
		}
	case enterModel:
		return &w.model
	case enterKey:
		return &w.key
	}
	return nil
}

func (w *wizard) inputs() []*textinput.Model {
	return []*textinput.Model{&w.name, &w.url, &w.model, &w.key}
}

func (w *wizard) focus() {
	for _, in := range w.inputs() {
		in.Blur()
	}
	if in := w.focused(); in != nil {
		in.Focus()
	}
}

func (w *wizard) save() tea.Cmd {
	key := token(w.key.Value())
	if key == "" {
		w.err = "An API key is required"
		return nil
	}
	choice := config.SetupChoice{APIKey: key, Model: token(w.model.Value())}
	if w.row == custom {
		choice.Provider = token(w.name.Value())
		choice.Type = w.protocols[w.protocol]
		choice.BaseURL = token(w.url.Value())
	} else {
		choice.Provider = providers[w.row].key
	}
	out, err := config.ApplySetup(choice)
	if err != nil {
		w.err = err.Error()
		return nil
	}
	w.result = Result{Saved: true, Provider: w.providerName(), Model: out.Model, Path: out.Path}
	w.done = true
	return tea.Quit
}

func (w *wizard) quit() tea.Cmd {
	w.done = true
	return tea.Quit
}

func (w *wizard) providerName() string {
	if w.row == custom {
		return token(w.name.Value())
	}
	return providers[w.row].name
}

// token is s without whitespace: every field takes a single word, and a
// pasted key often comes with a newline.
func token(s string) string { return strings.Join(strings.Fields(s), "") }

func (w *wizard) View() tea.View {
	if w.done {
		if !w.result.Saved {
			return tea.NewView("")
		}
		return tea.NewView(strings.Join([]string{
			"",
			" " + theme.OKText.Render("✓ ") + theme.Bold.Render(w.result.Provider) + theme.MutedText.Render(" is set up · "+w.result.Model),
			"   " + theme.SubtleText.Render("Saved to "+w.result.Path+" · /model switches models"),
			"",
		}, "\n"))
	}

	lines := []string{"", w.header(), ""}
	body, cursorAt := w.body()
	if cursorAt >= 0 {
		cursorAt += len(lines)
	}
	lines = append(lines, body...)
	if w.err != "" {
		lines = append(lines, "", theme.ErrorText.Render(w.err))
	}
	lines = append(lines, "", w.hint())

	width := w.width
	if width == 0 {
		width = 80
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(" "+l, width, "…")
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	if in := w.focused(); in != nil && cursorAt >= 0 {
		if c := in.Cursor(); c != nil {
			c.X += 1 + labelWidth
			c.Y = cursorAt
			v.Cursor = c
		}
	}
	return v
}

// header names the setup and the steps, the current one lit.
func (w *wizard) header() string {
	steps := []string{"Provider", "Model", "API key"}
	at := map[step]int{pickProvider: 0, describeCustom: 0, enterModel: 1, enterKey: 2}[w.step]
	for i, s := range steps {
		switch {
		case i == at:
			steps[i] = theme.AccentText.Bold(true).Render(s)
		case i < at:
			steps[i] = theme.MutedText.Render(s)
		default:
			steps[i] = theme.FaintText.Render(s)
		}
	}
	return theme.Bold.Render("codebot") + theme.SubtleText.Render(" setup   ") + strings.Join(steps, theme.FaintText.Render(" › "))
}

// labelWidth is the width of the mark and label before a field's value.
const labelWidth = 12

// body renders the step and the line of the focused field, -1 for none.
func (w *wizard) body() ([]string, int) {
	switch w.step {
	case pickProvider:
		out := []string{
			theme.Bold.Render("Choose your model provider"),
			theme.SubtleText.Render("One-time setup, saved to ~/.codebot/settings.json."),
			"",
		}
		for i, p := range providers {
			out = append(out, choice(fmt.Sprintf("%d  %s", i+1, p.name), "", i == w.row))
		}
		return append(out, choice(fmt.Sprintf("%d  Custom", custom+1), "any endpoint litellm speaks", w.row == custom)), -1

	case describeCustom:
		protocol := theme.FaintText.Render("‹ ") + theme.Text.Render(w.protocols[w.protocol]) + theme.FaintText.Render(" ›")
		out := []string{
			theme.Bold.Render("Describe your endpoint"),
			theme.SubtleText.Render("The name is how settings.json and /model refer to it."),
			"",
			field("Name", w.name.View(), w.field == fieldName),
			field("Protocol", protocol, w.field == fieldProtocol),
			field("Base URL", w.url.View(), w.field == fieldURL),
		}
		at := -1
		if w.field != fieldProtocol {
			at = 3 + w.field
		}
		return out, at

	case enterModel:
		return []string{
			theme.Bold.Render("Which model should codebot use?"),
			theme.SubtleText.Render("Its exact id, as the provider's docs give it."),
			"",
			field("Model", w.model.View(), true),
		}, 3

	default:
		return []string{
			theme.Bold.Render("Paste your " + w.providerName() + " API key"),
			theme.SubtleText.Render("It stays in ~/.codebot/settings.json on this machine."),
			"",
			field("API key", w.key.View(), true),
		}, 3
	}
}

func choice(text, detail string, selected bool) string {
	line := "  " + theme.Text.Render(text)
	if selected {
		line = theme.Selected.Render("❯ " + text)
	}
	if detail != "" {
		line += theme.SubtleText.Render("   " + detail)
	}
	return line
}

func field(label, value string, focused bool) string {
	mark, st := "  ", theme.MutedText
	if focused {
		mark, st = "❯ ", theme.Selected
	}
	return st.Render(mark+fmt.Sprintf("%-*s", labelWidth-2, label)) + value
}

func (w *wizard) hint() string {
	switch w.step {
	case pickProvider:
		return theme.Hint("↑↓", "select", "enter", "continue", "esc", "quit")
	case describeCustom:
		if w.field == fieldProtocol {
			return theme.Hint("←→", "protocol", "tab", "next", "enter", "continue", "esc", "back")
		}
		return theme.Hint("tab", "next", "enter", "continue", "esc", "back")
	case enterModel:
		return theme.Hint("enter", "continue", "esc", "back")
	}
	return theme.Hint("enter", "save", "esc", "back")
}
