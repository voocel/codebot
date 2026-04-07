package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/apperr"
)

// AskUserResponse carries answers and optional user notes.
type AskUserResponse struct {
	Answers map[string]string // question text → answer
	Notes   map[string]string // question text → user note (from "Type something" or preview notes)
}

// AskUserHandler blocks until the user answers all questions.
// Returns answers and notes. Returns error on cancel or timeout.
type AskUserHandler func(ctx context.Context, questions []Question) (*AskUserResponse, error)

// Question is a single question with selectable options.
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect"`
}

// Option is a selectable choice for a question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"` // optional markdown preview content
}

// AskUserTool lets the LLM ask the user structured questions.
type AskUserTool struct {
	mu      sync.RWMutex
	handler AskUserHandler
}

// NewAskUser creates an AskUserTool with no handler (set later via SetHandler).
func NewAskUser() *AskUserTool {
	return &AskUserTool{}
}

// SetHandler sets the UI callback. Must be called before the tool is used.
func (t *AskUserTool) SetHandler(h AskUserHandler) {
	t.mu.Lock()
	t.handler = h
	t.mu.Unlock()
}

func (t *AskUserTool) Name() string  { return "ask_user" }
func (t *AskUserTool) Label() string { return "Ask User" }
func (t *AskUserTool) Description() string {
	return `Ask the user structured questions when you need clarification, want to validate assumptions, or need decisions on implementation choices. Users can select from predefined options or provide custom input via "Other".`
}

func (t *AskUserTool) Schema() map[string]any {
	option := schema.Object(
		schema.Property("label", schema.String("Display text (1-5 words)")).Required(),
		schema.Property("description", schema.String("What this option means")).Required(),
		schema.Property("preview", schema.String("Optional preview content (markdown) shown in a side panel when this option is focused")),
	)
	question := schema.Object(
		schema.Property("question", schema.String("The complete question to ask")).Required(),
		schema.Property("header", schema.String("Short tag label (max 12 chars)")).Required(),
		schema.Property("options", schema.Array("2-4 selectable options", option)).Required(),
		schema.Property("multiSelect", schema.Bool("Allow multiple selections")),
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
		return json.Marshal("AskUserQuestion requires an interactive terminal. Make your best judgment and proceed without asking.")
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
	for i, q := range questions {
		if q.Question == "" {
			return fmt.Errorf("question %d: question text is required", i+1)
		}
		if q.Header == "" {
			return fmt.Errorf("question %d: header is required", i+1)
		}
		if utf8.RuneCountInString(q.Header) > 12 {
			return fmt.Errorf("question %d: header %q exceeds 12 characters", i+1, q.Header)
		}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return fmt.Errorf("question %d: need 2-4 options, got %d", i+1, len(q.Options))
		}
		for j, opt := range q.Options {
			if opt.Label == "" {
				return fmt.Errorf("question %d option %d: label is required", i+1, j+1)
			}
			if opt.Description == "" {
				return fmt.Errorf("question %d option %d: description is required", i+1, j+1)
			}
		}
	}
	return nil
}

func formatAnswers(questions []Question, resp *AskUserResponse) string {
	if resp == nil || len(resp.Answers) == 0 {
		return "User provided no answers. Make your best judgment and proceed."
	}
	var parts []string
	for _, q := range questions {
		answer, ok := resp.Answers[q.Question]
		if !ok {
			continue
		}
		entry := fmt.Sprintf("%q=%q", q.Question, answer)
		if note, hasNote := resp.Notes[q.Question]; hasNote && note != "" {
			entry += " user notes: " + note
		}
		// Include preview content of the selected option for context.
		for _, opt := range q.Options {
			if opt.Label == answer && opt.Preview != "" {
				entry += " preview: " + opt.Preview
				break
			}
		}
		parts = append(parts, entry)
	}
	return fmt.Sprintf("User has answered your questions: %s. You can now continue with the user's answers in mind.", strings.Join(parts, ", "))
}
