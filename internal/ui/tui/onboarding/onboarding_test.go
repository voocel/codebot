package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/infra/config"
)

// fakeList answers every listing with its models or its error, and records
// what it was asked.
type fakeList struct {
	models []config.SetupModel
	err    error
	asked  []config.SetupChoice
}

func (f *fakeList) list(_ context.Context, c config.SetupChoice) ([]config.SetupModel, error) {
	f.asked = append(f.asked, c)
	return f.models, f.err
}

// home gives the test its own settings, and no provider keys in the
// environment.
func home(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, p := range providers {
		if p.env != "" {
			t.Setenv(p.env, "")
		}
	}
}

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

func send(w *wizard, msg tea.Msg) tea.Cmd {
	_, cmd := w.Update(msg)
	return cmd
}

// settle runs cmd and what follows from it, but not the spinner.
func settle(w *wizard, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			settle(w, c)
		}
	case spinMsg, tea.QuitMsg:
	default:
		settle(w, send(w, msg))
	}
}

func press(w *wizard, keys ...string) {
	for _, k := range keys {
		settle(w, send(w, keyPress(k)))
	}
}

func write(w *wizard, text string) {
	for _, r := range text {
		press(w, string(r))
	}
}

func screen(w *wizard) string { return ansi.Strip(w.View().Content) }

func saved(t *testing.T) config.Settings {
	t.Helper()
	data, err := os.ReadFile(config.UserSettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var s config.Settings
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetUpAProvider(t *testing.T) {
	home(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	f := &fakeList{models: []config.SetupModel{{ID: "claude-new", Name: "Claude New", Window: 200_000}, {ID: "claude-old"}}}
	w := newWizard(f.list, config.Settings{})
	send(w, tea.WindowSizeMsg{Width: 80, Height: 40})
	if s := screen(w); !strings.Contains(s, "Choose a provider") || !strings.Contains(s, "$ANTHROPIC_API_KEY found") || !strings.Contains(s, "runs locally · no key") {
		t.Fatalf("first step:\n%s", s)
	}

	press(w, "enter")
	if s := screen(w); !strings.Contains(s, "from $ANTHROPIC_API_KEY") || strings.Contains(s, "sk-env") {
		t.Fatalf("the key from the environment is not masked in:\n%s", s)
	}
	press(w, "enter")
	if len(f.asked) != 1 || f.asked[0].Provider != "anthropic" || f.asked[0].APIKey != "sk-env" {
		t.Fatalf("asked %+v", f.asked)
	}
	if s := screen(w); !strings.Contains(s, "2 available") || !strings.Contains(s, "200k  Claude New") {
		t.Fatalf("models:\n%s", s)
	}

	write(w, "old")
	if s := screen(w); strings.Contains(s, "claude-new") || !strings.Contains(s, "claude-old") || !strings.Contains(s, `Use "old"`) {
		t.Fatalf("filtered by old:\n%s", s)
	}
	press(w, "enter")
	if w.result != (Result{Saved: true, Provider: "Anthropic", Model: "claude-old"}) {
		t.Fatalf("result = %+v", w.result)
	}
	s := saved(t)
	if p := s.Providers["anthropic"]; *s.Provider != "anthropic" || *s.Model != "claude-old" || p.APIKey != "sk-env" || !slices.Equal(p.Models, []string{"claude-old"}) {
		t.Errorf("saved %+v, anthropic %+v", s, p)
	}
}

func TestARejectedKeyGoesBackToIt(t *testing.T) {
	home(t)
	f := &fakeList{err: litellm.NewError("anthropic", litellm.ErrorTypeAuth, "invalid x-api-key", nil)}
	w := newWizard(f.list, config.Settings{})
	press(w, "enter", "enter")
	if s := screen(w); !strings.Contains(s, "api key is required") || len(f.asked) != 0 {
		t.Fatalf("an empty key was listed with:\n%s", s)
	}

	// The key is masked, and a pasted one loses its newline.
	send(w, tea.PasteMsg{Content: "sk-bad\n"})
	if s := screen(w); strings.Contains(s, "sk-bad") || !strings.Contains(s, "••••••") {
		t.Fatalf("the key shows:\n%s", s)
	}
	press(w, "enter")
	if f.asked[0].APIKey != "sk-bad" {
		t.Errorf("listed with %q", f.asked[0].APIKey)
	}
	if s := screen(w); w.step != enterKey || !strings.Contains(s, "Anthropic rejected the key: invalid x-api-key") || !strings.Contains(s, "••••••") || !strings.Contains(s, "use it anyway") {
		t.Fatalf("after the rejection:\n%s", s)
	}

	// An edit answers the rejection: enter checks the key again.
	write(w, "x")
	if s := screen(w); strings.Contains(s, "rejected") || strings.Contains(s, "use it anyway") {
		t.Fatalf("the rejection outlived an edit:\n%s", s)
	}
	press(w, "enter")
	if len(f.asked) != 2 {
		t.Fatalf("listed %d times", len(f.asked))
	}
}

// A key may be barred from listing the models alone, as an OpenAI key can
// be, or an endpoint may take it as a header set in settings.json: enter goes
// on with a rejected key, and leaves the model to type.
func TestARejectedKeyCanBeUsedAnyway(t *testing.T) {
	home(t)
	f := &fakeList{err: litellm.NewError("openai", litellm.ErrorTypeAuth, "Missing scopes: api.model.read.", nil)}
	w := newWizard(f.list, config.Settings{})
	press(w, "down", "enter")
	write(w, "sk-restricted")
	press(w, "enter", "enter")
	if s := screen(w); w.step != pickModel || !strings.Contains(s, "OpenAI rejected the key: Missing scopes: api.model.read. Esc") {
		t.Fatalf("step %d after enter on the rejected key:\n%s", w.step, s)
	}
	if len(f.asked) != 1 {
		t.Errorf("listed %d times", len(f.asked))
	}
	write(w, "gpt-x")
	press(w, "enter")
	if w.result != (Result{Saved: true, Provider: "OpenAI", Model: "gpt-x"}) {
		t.Fatalf("result = %+v", w.result)
	}
}

// A re-run starts on the default provider with its saved key, or on the
// default endpoint as it is described.
func TestAReRunStartsFromTheSettings(t *testing.T) {
	home(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-env-0123456789abcdef")
	deepseek := "deepseek"
	w := newWizard((&fakeList{}).list, config.Settings{Provider: &deepseek, Providers: map[string]*config.ProviderConfig{
		"deepseek": {APIKey: "sk-saved-0123456789abcdef"},
	}})
	send(w, tea.WindowSizeMsg{Width: 80, Height: 40})
	if s := screen(w); !strings.Contains(s, "❯ DeepSeek") || !strings.Contains(s, "key saved") {
		t.Fatalf("providers:\n%s", s)
	}
	press(w, "enter")
	if s := screen(w); w.key.Value() != "sk-saved-0123456789abcdef" || !strings.Contains(s, "sk-sav…cdef · from settings.json") {
		t.Fatalf("key %q in:\n%s", w.key.Value(), s)
	}

	local := "local"
	w = newWizard((&fakeList{}).list, config.Settings{Provider: &local, Providers: map[string]*config.ProviderConfig{
		"local": {Type: "anthropic", BaseURL: "https://proxy.example", APIKey: "sk-local"},
	}})
	press(w, "enter")
	if s := screen(w); !strings.Contains(s, "local") || !strings.Contains(s, "‹ anthropic ›") || !strings.Contains(s, "https://proxy.example") {
		t.Fatalf("the endpoint:\n%s", s)
	}
	press(w, "tab", "tab", "enter")
	if w.step != enterKey || w.key.Value() != "sk-local" {
		t.Errorf("step %d, key %q", w.step, w.key.Value())
	}
}

// A key shows only its ends below the masked field, and a short one not
// even those.
func TestTheKeyShowsItsEnds(t *testing.T) {
	home(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-SECRETSECRETSECRET-WXYZ")
	w := newWizard((&fakeList{}).list, config.Settings{})
	send(w, tea.WindowSizeMsg{Width: 80, Height: 40})
	press(w, "enter")
	if s := screen(w); !strings.Contains(s, "sk-ant…WXYZ · from $ANTHROPIC_API_KEY") || strings.Contains(s, "SECRET") {
		t.Fatalf("the key from the environment:\n%s", s)
	}
	press(w, "ctrl+u")
	write(w, "sk-short")
	if s := screen(w); strings.Contains(s, "sk-sho") || strings.Contains(s, "from $") {
		t.Fatalf("a short key typed over it:\n%s", s)
	}
}

func TestModelsThatCannotBeListedAreTyped(t *testing.T) {
	home(t)
	f := &fakeList{err: errors.New("connection refused")}
	w := newWizard(f.list, config.Settings{})
	press(w, "down", "enter")
	write(w, "sk-openai")
	press(w, "enter")
	if s := screen(w); !strings.Contains(s, "Type the model id") || !strings.Contains(s, "could not be listed: connection refused") {
		t.Fatalf("after the failed listing:\n%s", s)
	}
	press(w, "enter")
	if w.result.Saved {
		t.Fatal("saved without a model")
	}
	write(w, "gpt-x")
	press(w, "enter")
	if w.result != (Result{Saved: true, Provider: "OpenAI", Model: "gpt-x"}) {
		t.Fatalf("result = %+v", w.result)
	}
}

func TestOllamaNeedsNoKey(t *testing.T) {
	home(t)
	f := &fakeList{models: []config.SetupModel{{ID: "llama3"}}}
	w := newWizard(f.list, config.Settings{})
	for range len(providers) - 1 {
		press(w, "down")
	}
	press(w, "enter")
	if w.step != pickModel || len(f.asked) != 1 || f.asked[0] != (config.SetupChoice{Provider: "ollama"}) {
		t.Fatalf("step %d, asked %+v", w.step, f.asked)
	}
	press(w, "esc")
	if w.step != pickProvider {
		t.Fatalf("esc went to step %d, not the providers", w.step)
	}
	press(w, "enter", "enter")
	if p := saved(t).Providers["ollama"]; p.APIKey != "" || !slices.Equal(p.Models, []string{"llama3"}) {
		t.Errorf("saved %+v", p)
	}
}

func TestSetUpACustomEndpoint(t *testing.T) {
	home(t)
	f := &fakeList{models: []config.SetupModel{{ID: "qwen-local"}}}
	w := newWizard(f.list, config.Settings{})
	send(w, tea.WindowSizeMsg{Width: 80, Height: 40})
	for range custom {
		press(w, "down")
	}
	press(w, "enter", "enter", "enter", "enter")
	if !strings.Contains(screen(w), "A name is required") {
		t.Fatalf("an empty name went through:\n%s", screen(w))
	}
	write(w, "local")
	if strings.Contains(screen(w), "A name is required") {
		t.Fatalf("the error outlived the name:\n%s", screen(w))
	}
	press(w, "tab")
	if s := screen(w); !strings.Contains(s, "OpenAI-compatible Chat Completions") || !strings.Contains(s, "https://…/v1") {
		t.Fatalf("the protocol:\n%s", s)
	}
	press(w, "left")
	if s := screen(w); !strings.Contains(s, "‹ anthropic ›") || !strings.Contains(s, "(it adds /v1/messages)") {
		t.Fatalf("anthropic:\n%s", s)
	}
	press(w, "right", "tab")
	write(w, "localhost:8080")
	press(w, "enter")
	if !strings.Contains(screen(w), "must be an http(s) URL") {
		t.Fatalf("a URL without a scheme went through:\n%s", screen(w))
	}
	press(w, "ctrl+u")
	write(w, "http://localhost:8080/v1")
	press(w, "enter")

	// The endpoint's key is optional.
	press(w, "enter")
	if len(f.asked) != 1 || f.asked[0] != (config.SetupChoice{Provider: "local", Type: "compat", BaseURL: "http://localhost:8080/v1"}) {
		t.Fatalf("asked %+v", f.asked)
	}
	press(w, "enter")
	if w.result != (Result{Saved: true, Provider: "local", Model: "qwen-local"}) {
		t.Fatalf("result = %+v", w.result)
	}
	if p := saved(t).Providers["local"]; p.Type != "compat" || p.BaseURL != "http://localhost:8080/v1" || p.APIKey != "" {
		t.Errorf("saved %+v", p)
	}
}

func TestBackDropsTheListingInFlight(t *testing.T) {
	home(t)
	f := &fakeList{models: []config.SetupModel{{ID: "claude-x"}}}
	w := newWizard(f.list, config.Settings{})
	press(w, "enter")
	write(w, "sk-a")
	listing := send(w, keyPress("enter"))
	if !strings.Contains(screen(w), "Checking the key") {
		t.Fatalf("while listing:\n%s", screen(w))
	}
	press(w, "esc")
	settle(w, listing)
	if w.step != enterKey || w.models != nil {
		t.Fatalf("a dropped listing landed: step %d, models %v", w.step, w.models)
	}
	if w.key.Value() != "sk-a" {
		t.Errorf("back lost the key: %q", w.key.Value())
	}
	// The spinner of the dropped listing stops; the new one's goes on.
	send(w, keyPress("enter"))
	if send(w, spinMsg{1}) != nil || send(w, spinMsg{2}) == nil {
		t.Error("the spinners are mixed up")
	}
	press(w, "esc", "esc", "down", "enter")
	if w.key.Value() != "" {
		t.Errorf("OpenAI got Anthropic's key %q", w.key.Value())
	}
}

func TestTheModelListScrolls(t *testing.T) {
	home(t)
	f := &fakeList{}
	for i := range 20 {
		f.models = append(f.models, config.SetupModel{ID: fmt.Sprintf("m%02d", i)})
	}
	w := newWizard(f.list, config.Settings{})
	send(w, tea.WindowSizeMsg{Width: 80, Height: 40})
	press(w, "enter")
	write(w, "k")
	press(w, "enter")
	if s := screen(w); !strings.Contains(s, "m07") || strings.Contains(s, "m08") || !strings.Contains(s, "↓ 12 more") {
		t.Fatalf("first page:\n%s", s)
	}
	press(w, "up")
	if s := screen(w); !strings.Contains(s, "❯ m19") || strings.Contains(s, "m11") {
		t.Fatalf("up from the first wraps to the last:\n%s", s)
	}
	write(w, "m1")
	if s := screen(w); !strings.Contains(s, "❯ m10") {
		t.Fatalf("the filter keeps a highlight past its rows:\n%s", s)
	}
	press(w, "ctrl+u")
	write(w, "M01")
	if s := screen(w); !strings.Contains(s, "❯ m01") || strings.Contains(s, "Use") {
		t.Fatalf("an id in other case:\n%s", s)
	}
}

// A long error, such as an HTML page, keeps to three lines and the field in
// view.
func TestALongErrorKeepsTheFieldInView(t *testing.T) {
	home(t)
	page := "<html>\n<head><title>404 Not Found</title></head>\n<body>" + strings.Repeat("<p>nothing here at all</p>\n", 60) + "</body></html>"
	w := newWizard((&fakeList{err: errors.New(page)}).list, config.Settings{})
	send(w, tea.WindowSizeMsg{Width: 80, Height: 24})
	press(w, "enter")
	write(w, "sk-a")
	press(w, "enter")
	v := w.View()
	lines := strings.Split(ansi.Strip(v.Content), "\n")
	if len(lines) > 24 || v.Cursor == nil || !strings.Contains(lines[v.Cursor.Y], "Model") {
		t.Fatalf("%d lines, cursor %+v:\n%s", len(lines), v.Cursor, strings.Join(lines, "\n"))
	}
	if n := strings.Count(v.Content, "nothing here"); n > 6 {
		t.Errorf("the error runs on: %d paragraphs shown", n)
	}
}

// The terminal cursor sits where the focused field's text starts, with the
// welcome above the steps or without it. The welcome shows only where the
// providers have room for it too, so it does not come and go between steps.
func TestTheCursorSitsInTheField(t *testing.T) {
	home(t)
	for _, height := range []int{40, 24, 12} {
		w := newWizard((&fakeList{}).list, config.Settings{})
		send(w, tea.WindowSizeMsg{Width: 100, Height: height})
		press(w, "enter")
		v := w.View()
		if v.Cursor == nil {
			t.Fatalf("height %d: no cursor", height)
		}
		lines := strings.Split(ansi.Strip(v.Content), "\n")
		if got := ansi.Cut(lines[v.Cursor.Y], v.Cursor.X, v.Cursor.X+13); got != "paste it here" {
			t.Errorf("height %d: the cursor is at %q in\n%s", height, got, strings.Join(lines, "\n"))
		}
		send(w, tea.PasteMsg{Content: "sk-key"})
		if l := ansi.Strip(strings.Split(w.View().Content, "\n")[v.Cursor.Y]); strings.Contains(l, "…") {
			t.Errorf("height %d: the card cuts the field: %q", height, l)
		}
		if welcome := strings.Contains(v.Content, "Welcome to codebot"); welcome != (height == 40) {
			t.Errorf("height %d: welcome shown = %v", height, welcome)
		}
	}
}
