package tui

// onboarding.go — first-run setup wizard. A standalone Bubble Tea program run
// by main() before the runtime boots, sharing the welcome card's chrome
// (frameCard, theme tokens). Pure presentation: detection and persistence
// live in internal/config (NeedsSetup / ApplySetup).

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/provider"
)

// OnboardingResult reports what the wizard did. Saved=false means the user
// cancelled; the caller decides whether to continue booting.
type OnboardingResult struct {
	Saved    bool
	Provider string
	Model    string
	Path     string
}

// RunOnboarding runs the interactive first-run wizard and blocks until the
// user saves a provider configuration or cancels.
func RunOnboarding() (OnboardingResult, error) {
	final, err := tea.NewProgram(newOnboardModel()).Run()
	if err != nil {
		return OnboardingResult{}, fmt.Errorf("run setup: %w", err)
	}
	if m, ok := final.(*onboardModel); ok {
		return m.result, nil
	}
	return OnboardingResult{}, nil
}

type onboardStep int

const (
	stepProvider onboardStep = iota
	stepCustom
	stepModel
	stepAPIKey
)

// Custom-provider field indices (stepCustom).
const (
	customName = iota
	customType
	customURL
)

// onboardRow is one selectable entry on the provider step.
type onboardRow struct {
	key    string // provider key; empty for the custom entry
	name   string
	hint   string // short description; only the custom entry carries one
	custom bool
}

var onboardPresets = []struct{ key, name string }{
	{"openai", "OpenAI"},
	{"anthropic", "Anthropic"},
	{"gemini", "Google Gemini"},
	{"openrouter", "OpenRouter"},
	{"deepseek", "DeepSeek"},
}

type onboardModel struct {
	width int

	step   onboardStep
	rows   []onboardRow
	cursor int

	// Custom-provider fields.
	field   int
	name    string
	types   []string
	typeIdx int
	baseURL string

	model    string // model id buffer; required, never prefilled (defaults go stale)
	modelFor string // provider identity the buffer was typed for; changes clear it
	key      string // API key buffer, rendered masked
	errMsg   string

	done   bool
	result OnboardingResult
}

func newOnboardModel() *onboardModel {
	rows := make([]onboardRow, 0, len(onboardPresets)+1)
	for _, p := range onboardPresets {
		rows = append(rows, onboardRow{key: p.key, name: p.name})
	}
	rows = append(rows, onboardRow{name: "Custom", hint: "any endpoint litellm speaks", custom: true})

	types := provider.SupportedTypeNames()
	typeIdx := 0
	for i, t := range types {
		if t == "openai" {
			typeIdx = i
			break
		}
	}
	return &onboardModel{rows: rows, types: types, typeIdx: typeIdx}
}

func (m *onboardModel) Init() tea.Cmd { return nil }

func (m *onboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.done = true
			return m, tea.Quit
		}
		switch m.step {
		case stepProvider:
			return m.updateProvider(msg)
		case stepCustom:
			return m.updateCustom(msg)
		case stepModel:
			return m.updateModel(msg)
		default:
			return m.updateAPIKey(msg)
		}
	}
	return m, nil
}

func (m *onboardModel) updateProvider(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.done = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "enter":
		return m.selectProvider()
	default:
		// Number shortcuts jump and select in one keypress.
		if len(msg.Runes) == 1 {
			if i := int(msg.Runes[0] - '1'); i >= 0 && i < len(m.rows) {
				m.cursor = i
				return m.selectProvider()
			}
		}
	}
	return m, nil
}

func (m *onboardModel) selectProvider() (tea.Model, tea.Cmd) {
	m.errMsg = ""
	if m.rows[m.cursor].custom {
		m.step = stepCustom
	} else {
		m.enterModelStep()
	}
	return m, nil
}

// enterModelStep moves forward into the model step. The buffer is never
// prefilled — hardcoded model defaults go stale — but a value typed for the
// same provider survives esc round-trips; picking a different provider (or
// protocol) clears it.
func (m *onboardModel) enterModelStep() {
	id := m.rows[m.cursor].key
	if m.rows[m.cursor].custom {
		id = "custom/" + m.types[m.typeIdx] + "/" + strings.TrimSpace(m.name)
	}
	if id != m.modelFor {
		m.model = ""
		m.modelFor = id
	}
	m.step = stepModel
}

func (m *onboardModel) updateCustom(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.step = stepProvider
		m.errMsg = ""
		return m, nil
	case "up", "shift+tab":
		if m.field > 0 {
			m.field--
		}
		return m, nil
	case "down", "tab":
		if m.field < customURL {
			m.field++
		}
		return m, nil
	case "left":
		if m.field == customType {
			m.typeIdx = (m.typeIdx + len(m.types) - 1) % len(m.types)
		}
		return m, nil
	case "right":
		if m.field == customType {
			m.typeIdx = (m.typeIdx + 1) % len(m.types)
		}
		return m, nil
	case "enter":
		if m.field < customURL {
			m.field++
			return m, nil
		}
		if strings.TrimSpace(m.name) == "" {
			m.errMsg = "name is required"
			m.field = customName
			return m, nil
		}
		m.errMsg = ""
		m.enterModelStep()
		return m, nil
	case "backspace":
		if buf := m.customBuf(); buf != nil {
			if r := []rune(*buf); len(r) > 0 {
				*buf = string(r[:len(r)-1])
			}
		}
		return m, nil
	}
	if buf := m.customBuf(); buf != nil && len(msg.Runes) > 0 {
		*buf += sanitizeInput(string(msg.Runes))
	}
	return m, nil
}

// customBuf returns the focused text buffer; nil on the protocol row.
func (m *onboardModel) customBuf() *string {
	switch m.field {
	case customName:
		return &m.name
	case customURL:
		return &m.baseURL
	}
	return nil
}

func (m *onboardModel) updateModel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.rows[m.cursor].custom {
			m.step = stepCustom
		} else {
			m.step = stepProvider
		}
		m.errMsg = ""
		return m, nil
	case "enter":
		if strings.TrimSpace(m.model) == "" {
			m.errMsg = "model is required"
			return m, nil
		}
		m.errMsg = ""
		m.step = stepAPIKey
		return m, nil
	case "backspace":
		if r := []rune(m.model); len(r) > 0 {
			m.model = string(r[:len(r)-1])
		}
		return m, nil
	case "ctrl+u":
		m.model = ""
		return m, nil
	}
	if len(msg.Runes) > 0 {
		m.model += sanitizeInput(string(msg.Runes))
	}
	return m, nil
}

func (m *onboardModel) updateAPIKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.step = stepModel
		m.errMsg = ""
		return m, nil
	case "enter":
		return m.save()
	case "backspace":
		if r := []rune(m.key); len(r) > 0 {
			m.key = string(r[:len(r)-1])
		}
		return m, nil
	case "ctrl+u":
		m.key = ""
		return m, nil
	}
	if len(msg.Runes) > 0 {
		m.key += sanitizeInput(string(msg.Runes))
	}
	return m, nil
}

// sanitizeInput keeps only printable, non-space runes. Every onboarding field
// is a single token (provider name, URL, model id, API key), and Windows
// console events can deliver NUL/control runes — not Unicode whitespace, so a
// Fields-style strip would let them through — that must never enter a buffer.
func sanitizeInput(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) && !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m *onboardModel) save() (tea.Model, tea.Cmd) {
	key := strings.TrimSpace(m.key)
	if key == "" {
		m.errMsg = "API key is required — paste one to continue"
		return m, nil
	}
	choice := config.SetupChoice{APIKey: key, Model: strings.TrimSpace(m.model)}
	if row := m.rows[m.cursor]; row.custom {
		choice.Provider = strings.TrimSpace(m.name)
		choice.Type = m.types[m.typeIdx]
		choice.BaseURL = strings.TrimSpace(m.baseURL)
	} else {
		choice.Provider = row.key
	}
	outcome, err := config.ApplySetup(choice)
	if err != nil {
		m.errMsg = err.Error()
		return m, nil
	}
	// Display name for the summary; the settings key is in outcome.Provider.
	display := m.rows[m.cursor].name
	if m.rows[m.cursor].custom {
		display = outcome.Provider
	}
	m.result = OnboardingResult{Saved: true, Provider: display, Model: outcome.Model, Path: outcome.Path}
	m.done = true
	return m, tea.Quit
}

func (m *onboardModel) View() string {
	if m.done {
		if !m.result.Saved {
			return ""
		}
		// Final frame stays in scrollback right above the welcome banner.
		return "\n" + ToolIconStyle.Render("  ✓ ") +
			WelcomeTitleStyle.Render(m.result.Provider) +
			WelcomeBodyStyle.Render(" configured · model "+m.result.Model) + "\n" +
			WelcomeMutedStyle.Render("    Saved to "+m.result.Path+" — /model to switch anytime.") + "\n\n"
	}

	width := 64
	if m.width > 0 {
		width = min(max(m.width-4, 52), 68)
	}
	innerW := width - 4

	var body []string
	switch m.step {
	case stepProvider:
		body = m.viewProvider(innerW)
	case stepCustom:
		body = m.viewCustom(innerW)
	case stepModel:
		body = m.viewModel(innerW)
	default:
		body = m.viewAPIKey(innerW)
	}

	titleTag := WelcomeKickerStyle.Render("codebot") + " " + ContextChipAccentStyle.Render("setup")
	return "\n" + frameCard(titleTag, body, width) + "\n" + InputHintStyle.Render("  "+m.hint()) + "\n"
}

func (m *onboardModel) viewProvider(innerW int) []string {
	body := []string{
		"",
		askQuestionStyle.Render("Choose your model provider"),
		askDescStyle.Render("One-time setup, saved to ~/.codebot/settings.json."),
		"",
	}
	for i, row := range m.rows {
		name := fmt.Sprintf("%-15s", row.name)
		hint := askDescStyle.Render(clampLine(row.hint, innerW-17))
		if i == m.cursor {
			body = append(body, askOptionActiveStyle.Render("❯ "+name)+hint)
		} else {
			body = append(body, askOptionInactiveStyle.Render("  "+name)+hint)
		}
	}
	return append(body, "")
}

func (m *onboardModel) viewCustom(innerW int) []string {
	head := func(i int, label string) string {
		text := fmt.Sprintf("%-11s", label)
		if m.field == i {
			return askOptionActiveStyle.Render("❯ " + text)
		}
		return askOptionInactiveStyle.Render("  " + text)
	}
	fieldW := fieldWidth(innerW)
	protoVal := WelcomeBodyStyle.Render(m.types[m.typeIdx])
	if m.field == customType {
		protoVal = WelcomeBodyStyle.Render("‹ " + m.types[m.typeIdx] + " ›")
	}

	body := []string{
		"",
		askQuestionStyle.Render("Custom provider"),
		askDescStyle.Render("Point codebot at any endpoint litellm speaks."),
		"",
		head(customName, "Name") + renderInput(m.name, "required — e.g. my-proxy", m.field == customName, fieldW),
		head(customType, "Protocol") + protoVal,
		head(customURL, "Base URL") + renderInput(m.baseURL, "optional", m.field == customURL, fieldW),
		"",
	}
	if m.errMsg != "" {
		body = append(body, ErrorStyle.Render(clampLine("✗ "+m.errMsg, innerW)), "")
	}
	return body
}

// displayName returns the chosen provider's human label — the preset name,
// or whatever the user typed for a custom endpoint.
func (m *onboardModel) displayName() string {
	if row := m.rows[m.cursor]; !row.custom {
		return row.name
	}
	return strings.TrimSpace(m.name)
}

func (m *onboardModel) viewModel(innerW int) []string {
	field := askOptionActiveStyle.Render("❯ ") + renderInput(m.model, "type the exact model id", true, fieldWidth(innerW))
	body := []string{
		"",
		askQuestionStyle.Render(m.displayName() + " model"),
		askDescStyle.Render(clampLine("The model id as listed by "+m.displayName()+".", innerW)),
		"",
		field,
		"",
	}
	if m.errMsg != "" {
		body = append(body, ErrorStyle.Render(clampLine("✗ "+m.errMsg, innerW)))
	} else {
		body = append(body, askHintStyle.Render("Switch anytime later with /model."))
	}
	return append(body, "")
}

func (m *onboardModel) viewAPIKey(innerW int) []string {
	title := m.displayName()

	field := askOptionActiveStyle.Render("❯ ") + renderInput(maskKey(m.key), "paste your API key", true, fieldWidth(innerW))
	if n := len([]rune(m.key)); n > 0 {
		field += askDescStyle.Render(fmt.Sprintf("  %d chars", n))
	}

	body := []string{
		"",
		askQuestionStyle.Render(title + " API key"),
		askDescStyle.Render("Input is hidden — paste away."),
		"",
		field,
		"",
	}
	if m.errMsg != "" {
		body = append(body, ErrorStyle.Render(clampLine("✗ "+m.errMsg, innerW)))
	} else {
		body = append(body, "")
	}
	return append(body, "")
}

func (m *onboardModel) hint() string {
	switch m.step {
	case stepProvider:
		return "↑↓ choose · enter next · esc quit"
	case stepCustom:
		return "↑↓ field · ←→ protocol · enter next · esc back"
	case stepModel:
		return "enter next · esc back · ctrl+u clear"
	default:
		return "enter save · esc back · ctrl+u clear"
	}
}

// Input-field styles: underline marks the field extent like a form blank —
// typed text bright, the empty tail and placeholder dim.
var (
	inputTextStyle        = lipgloss.NewStyle().Underline(true).Foreground(Text)
	inputBlankStyle       = lipgloss.NewStyle().Underline(true).Foreground(Subtle)
	inputPlaceholderStyle = lipgloss.NewStyle().Underline(true).Foreground(Subtle).Italic(true)
)

// fieldWidth sizes the single-line form fields so they read as a form blank
// rather than stretching across the whole card.
func fieldWidth(innerW int) int {
	return max(24, min(innerW-16, 36))
}

// renderInput draws a single-line form field: the value (long values keep
// their tail visible), a block cursor when focused, a dim placeholder while
// empty, and an underlined blank tail marking the field extent.
func renderInput(value, placeholder string, focused bool, width int) string {
	var b strings.Builder
	used := 0
	if value == "" {
		if focused {
			b.WriteString("█")
			used++
		}
		p := clampLine(placeholder, width-used)
		b.WriteString(inputPlaceholderStyle.Render(p))
		used += lipgloss.Width(p)
	} else {
		r := []rune(value)
		avail := width
		if focused {
			avail-- // room for the trailing cursor
		}
		if len(r) > avail {
			r = r[len(r)-avail:]
		}
		b.WriteString(inputTextStyle.Render(string(r)))
		used += len(r)
		if focused {
			b.WriteString("█")
			used++
		}
	}
	if pad := width - used; pad > 0 {
		b.WriteString(inputBlankStyle.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// maskKey renders an API key with the first and last four characters visible
// and the middle masked, the convention provider dashboards use. Keys too
// short to reveal anything safely stay fully masked.
func maskKey(key string) string {
	r := []rune(key)
	n := len(r)
	if n < 12 {
		return strings.Repeat("•", n)
	}
	return string(r[:4]) + strings.Repeat("•", min(n-8, 20)) + string(r[n-4:])
}

// clampLine truncates s to at most w display runes, ellipsis included.
func clampLine(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w <= 1 {
		return s
	}
	return string(r[:w-1]) + "…"
}
