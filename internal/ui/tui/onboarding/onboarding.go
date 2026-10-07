// Package onboarding is the first-run setup. It connects a model provider:
// the key is checked by listing the models it reaches, which are then
// offered to pick from, and config.ApplySetup saves the choice before the
// App boots.
package onboarding

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/litellm"
	llmprovider "github.com/voocel/litellm/provider"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/ui/tui/brand"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// Saved is false when the user quit without saving.
type Result struct {
	Saved    bool
	Provider string // as the user knows it, such as "Anthropic"
	Model    string
}

func Run() (Result, error) {
	theme.Detect()
	list := func(ctx context.Context, c config.SetupChoice) ([]config.SetupModel, error) { return c.Models(ctx) }
	final, err := tea.NewProgram(newWizard(list)).Run()
	if err != nil {
		return Result{}, fmt.Errorf("run setup: %w", err)
	}
	return final.(*wizard).result, nil
}

// lister lists the models a choice's key reaches; config.SetupChoice.Models
// outside tests.
type lister func(context.Context, config.SetupChoice) ([]config.SetupModel, error)

// listTimeout bounds the key check, which lists the models.
const listTimeout = 20 * time.Second

type provider struct {
	key, name string
	env       string // the environment variable that usually holds its key
	keys      string // where to create a key
	keyless   bool   // it runs locally and takes no key
}

var providers = []provider{
	{key: "anthropic", name: "Anthropic", env: "ANTHROPIC_API_KEY", keys: "platform.claude.com/settings/keys"},
	{key: "openai", name: "OpenAI", env: "OPENAI_API_KEY", keys: "platform.openai.com/api-keys"},
	{key: "gemini", name: "Google Gemini", env: "GEMINI_API_KEY", keys: "aistudio.google.com/apikey"},
	{key: "deepseek", name: "DeepSeek", env: "DEEPSEEK_API_KEY", keys: "platform.deepseek.com/api_keys"},
	{key: "openrouter", name: "OpenRouter", env: "OPENROUTER_API_KEY", keys: "openrouter.ai/settings/keys"},
	{key: "grok", name: "xAI Grok", env: "XAI_API_KEY", keys: "console.x.ai"},
	{key: "glm", name: "Zhipu GLM", env: "ZHIPUAI_API_KEY", keys: "bigmodel.cn/usercenter/proj-mgmt/apikeys"},
	{key: "qwen", name: "Qwen", env: "DASHSCOPE_API_KEY", keys: "bailian.console.aliyun.com"},
	{key: "minimax", name: "MiniMax", env: "MINIMAX_API_KEY", keys: "platform.minimax.io"},
	{key: "mimo", name: "Xiaomi MiMo", env: "MIMO_API_KEY", keys: "platform.xiaomimimo.com"},
	{key: "ollama", name: "Ollama", keyless: true},
}

// custom is the row after the listed providers: any other endpoint.
var custom = len(providers)

// protocolNotes explains the protocols an endpoint most often speaks.
var protocolNotes = map[string]string{
	"compat":    "OpenAI-compatible Chat Completions",
	"openai":    "OpenAI Responses",
	"anthropic": "Anthropic Messages",
	"gemini":    "Gemini generateContent",
}

type step int

const (
	pickProvider step = iota
	describeCustom
	enterKey
	pickModel
)

const (
	fieldName = iota
	fieldProtocol
	fieldURL
)

type wizard struct {
	list          lister
	width, height int
	step          step
	row           int // picked provider, or custom
	field         int // focused custom-endpoint field

	name, url, key, filter textinput.Model
	protocols              []string
	protocol               int
	keyFor                 int // the row the key was entered for

	// checking numbers the listing in flight, 0 when none, so a reply the
	// user went back from is dropped.
	checking, checks int
	models           []config.SetupModel
	listErr          error // why the models could not be listed
	at, top          int   // highlighted and first shown model row

	err    string
	result Result
}

func newWizard(list lister) *wizard {
	// Bedrock signs in with AWS keys, not an API key.
	protocols := slices.DeleteFunc(llmprovider.Names(), func(t string) bool { return t == "bedrock" })
	return &wizard{
		list:      list,
		name:      input("my-endpoint"),
		url:       input("https://…/v1"),
		key:       secret(),
		filter:    input("type to filter, or any model id"),
		protocols: protocols,
		protocol:  slices.Index(protocols, "compat"),
		keyFor:    -1,
	}
}

func input(placeholder string) textinput.Model {
	in := panel.NewInput(placeholder)
	in.SetVirtualCursor(false)
	return in
}

func secret() textinput.Model {
	in := input("paste it here")
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	return in
}

type (
	listedMsg struct {
		check  int
		models []config.SetupModel
		err    error
	}
	spinMsg struct{}
)

func (w *wizard) Init() tea.Cmd { return nil }

func (w *wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		for _, in := range w.inputs() {
			// An input draws a column past its width, for the cursor.
			in.SetWidth(max(brand.Inner(w.cardWidth())-labelWidth-1, 8))
		}
		return w, nil
	case listedMsg:
		w.listed(msg)
		return w, nil
	case spinMsg:
		if w.checking != 0 {
			return w, spin()
		}
		return w, nil
	case tea.KeyPressMsg:
		k := msg.String()
		if k == "ctrl+c" {
			return w, tea.Quit
		}
		if cmd, ok := w.press(k); ok {
			return w, cmd
		}
	}
	in := w.focused()
	if in == nil {
		return w, nil
	}
	before := in.Value()
	var cmd tea.Cmd
	*in, cmd = in.Update(msg)
	if in == &w.filter && in.Value() != before {
		w.at, w.top = 0, 0
	}
	return w, cmd
}

// press returns ok false for keys the focused field should handle.
func (w *wizard) press(k string) (cmd tea.Cmd, ok bool) {
	switch w.step {
	case pickProvider:
		switch k {
		case "up", "k":
			w.row = max(w.row-1, 0)
		case "down", "j":
			w.row = min(w.row+1, custom)
		case "enter":
			return w.pick(), true
		case "esc":
			return tea.Quit, true
		}
		return nil, true

	case describeCustom:
		switch k {
		case "esc":
			w.back()
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
			w.describe()
		default:
			return nil, false
		}
		w.focus()
		return nil, true

	case enterKey:
		switch k {
		case "esc":
			w.back()
		case "enter":
			return w.check(), true
		default:
			return nil, false
		}
		return nil, true

	default:
		rows := w.rows()
		switch k {
		case "esc":
			w.back()
		case "up":
			w.move(-1, len(rows))
		case "down":
			w.move(1, len(rows))
		case "enter":
			if len(rows) > 0 {
				return w.save(rows[w.at].id), true
			}
		default:
			// The filter waits for the models.
			return nil, w.checking != 0
		}
		return nil, true
	}
}

func (w *wizard) pick() tea.Cmd {
	switch {
	case w.row == custom:
		w.goTo(describeCustom)
	case w.keyless():
		w.fillKey()
		return w.check()
	default:
		w.goTo(enterKey)
	}
	return nil
}

// describe moves through the endpoint's fields, and on from the last one
// once they hold a name and an http(s) base URL.
func (w *wizard) describe() {
	u, err := url.Parse(token(w.url.Value()))
	switch {
	case w.field < fieldURL:
		w.field++
	case token(w.name.Value()) == "":
		w.err, w.field = "A name is required", fieldName
	case err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "":
		w.err = "The base URL must be an http(s) URL"
	default:
		w.goTo(enterKey)
	}
}

// back goes to the step before, leaving any listing in flight.
func (w *wizard) back() {
	w.checking = 0
	switch {
	case w.step == pickModel && !w.keyless():
		w.goTo(enterKey)
	case w.step == enterKey && w.row == custom:
		w.goTo(describeCustom)
	default:
		w.goTo(pickProvider)
	}
}

func (w *wizard) goTo(s step) {
	w.step, w.err = s, ""
	if s == enterKey {
		w.fillKey()
	}
	w.focus()
}

// fillKey drops another provider's key, which is of no use here; the
// environment may hold this one's.
func (w *wizard) fillKey() {
	if w.keyFor == w.row {
		return
	}
	w.keyFor = w.row
	w.key.Reset()
	if env := w.env(); env != "" {
		w.key.SetValue(os.Getenv(env))
		w.key.CursorEnd()
	}
}

func (w *wizard) keyless() bool { return w.row != custom && providers[w.row].keyless }

// env names the variable holding the picked provider's key, if it holds
// one.
func (w *wizard) env() string {
	if w.row == custom || os.Getenv(providers[w.row].env) == "" {
		return ""
	}
	return providers[w.row].env
}

// check builds the provider, which needs no request, then lists the models,
// which checks the key.
func (w *wizard) check() tea.Cmd {
	choice := w.choice()
	if err := choice.Check(); err != nil {
		w.err = err.Error()
		return nil
	}
	w.checks++
	w.checking = w.checks
	w.step, w.err = pickModel, ""
	w.models, w.listErr = nil, nil
	w.filter.Reset()
	w.at, w.top = 0, 0
	w.focus()
	n, list := w.checks, w.list
	return tea.Batch(spin(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		models, err := list(ctx, choice)
		return listedMsg{n, models, err}
	})
}

func spin() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

// listed takes the models in, or sends the user back to a key the provider
// rejected. A list that failed otherwise leaves the id to type, and so does
// a custom endpoint's rejection: the endpoint may take its key another way,
// such as a header set in settings.json, or guard only its model list.
func (w *wizard) listed(msg listedMsg) {
	if msg.check != w.checking {
		return
	}
	w.checking = 0
	if litellm.ErrorTypeOf(msg.err) == litellm.ErrorTypeAuth && w.row != custom {
		w.goTo(enterKey)
		w.err = w.rejection(msg.err)
		return
	}
	w.models, w.listErr = msg.models, msg.err
	w.focus()
}

func (w *wizard) rejection(err error) string {
	s := w.providerName() + " rejected the key"
	var e *litellm.Error
	if errors.As(err, &e) && e.Message != "" {
		s += ": " + strings.TrimSuffix(e.Message, ".")
	}
	return s
}

func (w *wizard) choice() config.SetupChoice {
	c := config.SetupChoice{APIKey: token(w.key.Value())}
	if w.row == custom {
		c.Provider, c.Type, c.BaseURL = token(w.name.Value()), w.protocols[w.protocol], token(w.url.Value())
	} else {
		c.Provider = providers[w.row].key
	}
	return c
}

type row struct {
	id, name string
	window   int
	typed    bool // the filter as typed, which names no listed model
}

// rows are the models the filter matches, then the filter itself unless it
// names one exactly.
func (w *wizard) rows() []row {
	if w.checking != 0 {
		return nil
	}
	typed := token(w.filter.Value())
	q := strings.ToLower(typed)
	var out []row
	exact := false
	for _, m := range w.models {
		if strings.Contains(strings.ToLower(m.ID), q) || strings.Contains(strings.ToLower(m.Name), q) {
			out = append(out, row{id: m.ID, name: m.Name, window: m.Window})
		}
		exact = exact || m.ID == typed
	}
	if typed != "" && !exact {
		out = append(out, row{id: typed, typed: true})
	}
	return out
}

// shown is how many model rows the list shows at once.
const shown = 8

func (w *wizard) move(by, n int) {
	if n == 0 {
		return
	}
	w.at = (w.at + by + n) % n
	if w.at < w.top {
		w.top = w.at
	} else if w.at >= w.top+shown {
		w.top = w.at - shown + 1
	}
}

func (w *wizard) save(model string) tea.Cmd {
	c := w.choice()
	c.Model = model
	if err := config.ApplySetup(c); err != nil {
		w.err = err.Error()
		return nil
	}
	w.result = Result{Saved: true, Provider: w.providerName(), Model: model}
	return tea.Quit
}

func (w *wizard) focused() *textinput.Model {
	switch w.step {
	case describeCustom:
		switch w.field {
		case fieldName:
			return &w.name
		case fieldURL:
			return &w.url
		}
	case enterKey:
		return &w.key
	case pickModel:
		if w.checking == 0 {
			return &w.filter
		}
	}
	return nil
}

func (w *wizard) inputs() []*textinput.Model {
	return []*textinput.Model{&w.name, &w.url, &w.key, &w.filter}
}

func (w *wizard) focus() {
	for _, in := range w.inputs() {
		in.Blur()
	}
	if in := w.focused(); in != nil {
		in.Focus()
	}
}

func (w *wizard) providerName() string {
	if w.row == custom {
		return token(w.name.Value())
	}
	return providers[w.row].name
}

// glimpse shows a key's first 6 and last 4 characters, enough to tell it
// from another; a key under 20 shows none, as they would give much of it
// away.
func glimpse(key string) string {
	r := []rune(key)
	if len(r) < 20 {
		return ""
	}
	return string(r[:6]) + "…" + string(r[len(r)-4:])
}

// token strips all whitespace: every field is a single word, and a pasted
// key often ends with a newline.
func token(s string) string { return strings.Join(strings.Fields(s), "") }

const (
	maxCardWidth = 72
	labelWidth   = 12
	headGap      = 4 // columns between the mark and the text beside it
)

func (w *wizard) cardWidth() int { return min(cmp.Or(w.width, 80), maxCardWidth) }

func (w *wizard) View() tea.View {
	width, height := cmp.Or(w.width, 80), cmp.Or(w.height, 24)
	cw := w.cardWidth()
	body, at := w.body(brand.Inner(cw))

	// The welcome sits above the steps when there is room for it.
	var lines []string
	cursor := -1
	for _, roomy := range []bool{true, false} {
		k := brand.NewCard(cw)
		if roomy {
			k.Add("")
			k.Add(brand.Beside([]string{
				theme.Bold.Foreground(theme.Strong).Render("Welcome to codebot"),
				theme.MutedText.Render("Connect a model to get started"),
				theme.SubtleText.Render("Saved to " + transcript.ShortPath(config.UserSettingsPath())),
			}, headGap)...)
			k.Add("")
		}
		k.Section(w.steps(), "")
		k.Add("")
		if at >= 0 {
			cursor = k.Lines() + at
		}
		k.Add(body...)
		k.Add("")
		lines = k.Close()
		if len(lines)+2 <= height {
			break
		}
	}
	lines = append(lines, "", strings.Repeat(" ", 1+brand.Pad)+w.hint())
	top := max(height-len(lines), 0) * 2 / 5
	screen := append(make([]string, top), brand.Center(lines, width)...)

	v := tea.NewView(strings.Join(screen, "\n"))
	v.AltScreen = true
	if in := w.focused(); in != nil && cursor >= 0 {
		if c := in.Cursor(); c != nil {
			c.X += max(width-cw, 0)/2 + 1 + brand.Pad + labelWidth
			c.Y = top + cursor
			v.Cursor = c
		}
	}
	return v
}

// steps is the stepper on the card's divider.
func (w *wizard) steps() string {
	at := 0
	switch w.step {
	case enterKey:
		at = 1
	case pickModel:
		at = 2
	}
	names := []string{"Provider", "API key", "Model"}
	for i, s := range names {
		switch {
		case i == at:
			names[i] = theme.AccentText.Bold(true).Render(s)
		case i < at:
			names[i] = theme.MutedText.Render(s)
		default:
			names[i] = theme.FaintText.Render(s)
		}
	}
	return strings.Join(names, theme.FaintText.Render(" › "))
}

// body returns the step's lines and the line of the focused field, -1 for
// none.
func (w *wizard) body(width int) ([]string, int) {
	var out []string
	at := -1
	switch w.step {
	case pickProvider:
		const other = "Other endpoint"
		names := len(other)
		for _, p := range providers {
			names = max(names, len(p.name))
		}
		out = append(out, theme.Bold.Render("Choose a provider"), "")
		for i, p := range providers {
			detail := ""
			switch {
			case p.keyless:
				detail = theme.SubtleText.Render("runs locally · no key")
			case os.Getenv(p.env) != "":
				detail = theme.OKText.Render("$" + p.env + " found")
			}
			out = append(out, choice(fmt.Sprintf("%-*s", names, p.name), detail, i == w.row))
		}
		out = append(out, choice(other, theme.SubtleText.Render("a proxy, gateway or local server"), w.row == custom))

	case describeCustom:
		p := w.protocols[w.protocol]
		protocol := theme.FaintText.Render("‹ ") + theme.Text.Render(p) + theme.FaintText.Render(" ›")
		if note := protocolNotes[p]; note != "" {
			protocol += theme.SubtleText.Render("  " + note)
		}
		out = append(out,
			theme.Bold.Render("Describe the endpoint"),
			theme.SubtleText.Render("Its name is how settings.json and /model call it."),
			"",
			field("Name", w.name.View(), w.field == fieldName),
			field("Protocol", protocol, w.field == fieldProtocol),
			field("Base URL", w.url.View(), w.field == fieldURL),
		)
		if w.field != fieldProtocol {
			at = 3 + w.field
		}

	case enterKey:
		if w.row == custom {
			out = append(out, theme.Bold.Render("API key for "+w.providerName()), theme.SubtleText.Render("Leave it empty if the endpoint takes none."))
		} else {
			out = append(out, theme.Bold.Render("Paste your "+w.providerName()+" API key"), theme.SubtleText.Render("Create one at "+providers[w.row].keys))
		}
		out = append(out, "", field("API key", w.key.View(), false))
		at = 3
		var about []string
		if g := glimpse(token(w.key.Value())); g != "" {
			about = append(about, g)
		}
		if env := w.env(); env != "" && token(w.key.Value()) == token(os.Getenv(env)) {
			about = append(about, "from $"+env)
		}
		if len(about) > 0 {
			out = append(out, strings.Repeat(" ", labelWidth)+theme.SubtleText.Render(strings.Join(about, " · ")))
		}

	default:
		out, at = w.modelBody(width)
	}
	if w.err != "" {
		out = append(out, "")
		for _, l := range strings.Split(ansi.Wordwrap(w.err, width, " "), "\n") {
			out = append(out, theme.ErrorText.Render(l))
		}
	}
	return out, at
}

func (w *wizard) modelBody(width int) ([]string, int) {
	if w.checking != 0 {
		doing := "Checking the key and listing the models"
		if w.keyless() {
			doing = "Asking " + w.providerName() + " for its models"
		}
		return []string{
			theme.Bold.Render("Choose a model"),
			"",
			theme.WarmText.Render(transcript.Spinner(time.Now()) + " " + doing + "…"),
		}, -1
	}
	var out []string
	if w.listErr != nil {
		why := "The models could not be listed: " + w.listErr.Error()
		if litellm.ErrorTypeOf(w.listErr) == litellm.ErrorTypeAuth {
			why = w.rejection(w.listErr) + ". Esc changes it."
		}
		out = append(out, theme.Bold.Render("Type the model id"))
		for _, l := range strings.Split(ansi.Wordwrap(why, width, " "), "\n") {
			out = append(out, theme.WarmText.Render(l))
		}
	} else {
		out = append(out, theme.Bold.Render("Choose a model")+theme.SubtleText.Render(fmt.Sprintf(" · %d available", len(w.models))))
	}
	out = append(out, "", field("Model", w.filter.View(), false), "")
	at := len(out) - 2

	rows := w.rows()
	idWidth := 0
	for _, r := range rows {
		if !r.typed {
			idWidth = max(idWidth, ansi.StringWidth(r.id))
		}
	}
	idWidth = min(idWidth, width/2)
	for i := w.top; i < min(w.top+shown, len(rows)); i++ {
		out = append(out, modelRow(rows[i], i == w.at, idWidth))
	}
	if more := len(rows) - w.top - shown; more > 0 {
		out = append(out, theme.FaintText.Render(fmt.Sprintf("  ↓ %d more", more)))
	}
	if len(rows) == 0 && w.listErr == nil {
		out = append(out, theme.SubtleText.Render("  No model matches"))
	}
	return out, at
}

// modelRow lines up the ids, windows and names; the card cuts what runs
// past its edge.
func modelRow(r row, selected bool, idWidth int) string {
	if r.typed {
		return choice(`Use "`+r.id+`"`, theme.SubtleText.Render("as typed"), selected)
	}
	id := ansi.Truncate(r.id, idWidth, "…")
	window := ""
	if r.window > 0 {
		window = transcript.Tokens(r.window)
	}
	detail := theme.FaintText.Render(fmt.Sprintf("%5s", window))
	if r.name != "" && r.name != r.id {
		detail += "  " + theme.SubtleText.Render(r.name)
	}
	return choice(id+strings.Repeat(" ", idWidth-ansi.StringWidth(id)), detail, selected)
}

func choice(text, detail string, selected bool) string {
	line := "  " + theme.Text.Render(text)
	if selected {
		line = theme.Selected.Render("❯ " + text)
	}
	if detail != "" {
		line += "   " + detail
	}
	return line
}

// field marks the focused one of several fields; a step with one shows
// only the cursor in it.
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
	case enterKey:
		return theme.Hint("enter", "check the key", "esc", "back")
	}
	if w.checking != 0 {
		return theme.Hint("esc", "back")
	}
	return theme.Hint("↑↓", "select", "enter", "save", "esc", "back")
}
