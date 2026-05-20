package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/voocel/agentcore/permission"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/apperr"
)

// AskUserResponse carries the outcome of an ask_user interaction.
//
// Answers is always populated: an empty []string means the question was shown
// but not answered (only possible when Cancelled is true). Notes carries the
// raw text the user typed into "Type your own answer" — never echoed in the
// answer list, surfaced separately so the model sees it as user-authored.
type AskUserResponse struct {
	Answers   map[string][]string // question text → selected labels (or user-typed text)
	Notes     map[string]string   // question text → custom text the user typed
	Cancelled bool                // true if the user dismissed the dialog before submitting
}

// AskUserHandler blocks until the user submits or dismisses the dialog.
// On dismiss, returns a Response with Cancelled=true (not an error) so partial
// answers can still reach the model.
type AskUserHandler func(ctx context.Context, questions []Question) (*AskUserResponse, error)

// Question is a single multi-choice question.
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	// Custom controls whether the host renders a built-in "Type your own
	// answer" entry below the listed options. Nil = default true.
	Custom *bool `json:"custom,omitempty"`
}

// Option is a selectable choice for a question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"`
}

// AskUserTool lets the model ask the user structured multi-choice questions.
type AskUserTool struct {
	mu      sync.RWMutex
	handler AskUserHandler
}

// NewAskUser creates an AskUserTool with no handler. Call SetHandler before use.
func NewAskUser() *AskUserTool { return &AskUserTool{} }

// SetHandler installs the UI callback. Calling with nil disables the tool.
func (t *AskUserTool) SetHandler(h AskUserHandler) {
	t.mu.Lock()
	t.handler = h
	t.mu.Unlock()
}

func (t *AskUserTool) Name() string  { return "ask_user" }
func (t *AskUserTool) Label() string { return "Ask User" }
func (t *AskUserTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityInternal}
}
func (t *AskUserTool) Description() string {
	return `Ask the user structured multi-choice questions when you need to clarify intent, validate assumptions, or pick between approaches.

Conventions:
- Provide 2-4 options per question; the host automatically appends a "Type your own answer" entry unless you set "custom": false. Do NOT add "Other" or catch-all options yourself.
- Set "multiSelect": true when answers are not mutually exclusive.
- If you recommend a specific option, put it first and suffix the label with "(Recommended)".
- Question texts must be unique across the call; option labels must be unique within each question.

Plan mode: in plan mode, use this tool to clarify requirements or choose between approaches BEFORE finalizing your plan. Do NOT use this tool to ask "Is my plan ready?" or "Should I proceed?" — that is exit_plan_mode's job. IMPORTANT: do not reference "the plan" in your questions (e.g. "Does this plan look good?"), because the user cannot see your plan until you call exit_plan_mode.`
}

func (t *AskUserTool) Schema() map[string]any {
	option := schema.Object(
		schema.Property("label", schema.String("Display text (1-5 words)")).Required(),
		schema.Property("description", schema.String("What this option means")).Required(),
		schema.Property("preview", schema.String("Optional preview content shown in a side panel when this option is focused")),
	)
	question := schema.Object(
		schema.Property("question", schema.String("The complete question to ask")).Required(),
		schema.Property("header", schema.String("Short tag label (max 12 chars)")).Required(),
		schema.Property("options", schema.Array("2-4 selectable options", option)).Required(),
		schema.Property("multiSelect", schema.Bool("Allow multiple selections")),
		schema.Property("custom", schema.Bool("Allow free-text answer (default true)")),
	)
	return schema.Object(
		schema.Property("questions", schema.Array("1-4 questions to ask the user", question)).Required(),
	)
}

type askUserArgs struct {
	Questions []Question `json:"questions"`
}

func (t *AskUserTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a askUserArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, apperr.WrapKind(apperr.KindToolInput, "invalid args", err)
	}

	if err := validateQuestions(a.Questions); err != nil {
		return json.Marshal(fmt.Sprintf("Validation error: %s", err))
	}

	t.mu.RLock()
	h := t.handler
	t.mu.RUnlock()

	if h == nil {
		return json.Marshal("ask_user is unavailable in this run (no interactive terminal). Make your best judgment and proceed.")
	}

	resp, err := h(ctx, a.Questions)
	if err != nil {
		return json.Marshal(fmt.Sprintf("User interaction failed: %s. Make your best judgment and proceed.", err))
	}

	return json.Marshal(formatAnswers(a.Questions, resp))
}

func validateQuestions(questions []Question) error {
	if len(questions) == 0 {
		return fmt.Errorf("at least one question is required")
	}
	if len(questions) > 4 {
		return fmt.Errorf("at most 4 questions allowed, got %d", len(questions))
	}
	seenQ := make(map[string]struct{}, len(questions))
	for i, q := range questions {
		if q.Question == "" {
			return fmt.Errorf("question %d: question text is required", i+1)
		}
		if _, dup := seenQ[q.Question]; dup {
			return fmt.Errorf("question %d: duplicate question text", i+1)
		}
		seenQ[q.Question] = struct{}{}
		if q.Header == "" {
			return fmt.Errorf("question %d: header is required", i+1)
		}
		if utf8.RuneCountInString(q.Header) > 12 {
			return fmt.Errorf("question %d: header %q exceeds 12 characters", i+1, q.Header)
		}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return fmt.Errorf("question %d: need 2-4 options, got %d", i+1, len(q.Options))
		}
		seenL := make(map[string]struct{}, len(q.Options))
		for j, opt := range q.Options {
			if opt.Label == "" {
				return fmt.Errorf("question %d option %d: label is required", i+1, j+1)
			}
			if _, dup := seenL[opt.Label]; dup {
				return fmt.Errorf("question %d option %d: duplicate label %q", i+1, j+1, opt.Label)
			}
			seenL[opt.Label] = struct{}{}
			if opt.Description == "" {
				return fmt.Errorf("question %d option %d: description is required", i+1, j+1)
			}
		}
	}
	return nil
}

// AllowsCustom reports whether the host should render a built-in
// "Type your own answer" entry for this question.
func (q Question) AllowsCustom() bool {
	return q.Custom == nil || *q.Custom
}

// formatAnswers turns a response into the text the model sees.
// Submit and Cancel share this path; Cancelled flips the framing and includes
// "(unanswered)" placeholders so partial context still flows back.
func formatAnswers(questions []Question, resp *AskUserResponse) string {
	if resp == nil {
		return "User provided no answers. Make your best judgment and proceed."
	}

	parts := make([]string, 0, len(questions))
	anyAnswered := false
	for _, q := range questions {
		answers := resp.Answers[q.Question]
		if len(answers) == 0 {
			if resp.Cancelled {
				parts = append(parts, fmt.Sprintf("%q=(unanswered)", q.Question))
			}
			continue
		}
		anyAnswered = true
		entry := fmt.Sprintf("%q=%s", q.Question, formatAnswerList(answers))
		if note := resp.Notes[q.Question]; note != "" {
			entry += " note: " + note
		}
		if preview := pickPreview(q, answers); preview != "" {
			entry += "\nselected preview:\n" + preview
		}
		parts = append(parts, entry)
	}

	if resp.Cancelled {
		if !anyAnswered {
			return "User cancelled the questions before answering any. Make your best judgment and proceed."
		}
		return "User cancelled the questions before finishing. Partial answers:\n" +
			strings.Join(parts, "\n") +
			"\nProceed with your best judgment."
	}

	if len(parts) == 0 {
		return "User provided no answers. Make your best judgment and proceed."
	}
	return "User has answered your questions: " + strings.Join(parts, "; ") +
		". You can now continue with the user's answers in mind."
}

func formatAnswerList(answers []string) string {
	if len(answers) == 1 {
		return fmt.Sprintf("%q", answers[0])
	}
	quoted := make([]string, len(answers))
	for i, a := range answers {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// pickPreview returns the preview of the first matched listed option. Custom
// answers (user-typed text) have no preview to surface.
func pickPreview(q Question, answers []string) string {
	for _, a := range answers {
		for _, opt := range q.Options {
			if opt.Label == a && opt.Preview != "" {
				return opt.Preview
			}
		}
	}
	return ""
}
