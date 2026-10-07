package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"

	"github.com/voocel/codebot/internal/interact"
)

type askUserArgs struct {
	Questions []interact.Question `json:"questions"`
}

func NewAskUser(ui interact.UI) agentcore.Tool {
	tool := agentcore.NewTool("ask_user", askUserDescription, askUserSchema(), func(ctx context.Context, a askUserArgs) (agentcore.Result, error) {
		answers, err := ui.Ask(ctx, a.Questions)
		if errors.Is(err, interact.ErrUnsupported) {
			return agentcore.TextResult("ask_user is unavailable in this run (no interactive user). Make your best judgment and proceed."), nil
		}
		if err != nil {
			return agentcore.Result{}, err
		}
		return agentcore.TextResult(formatAnswers(a.Questions, answers)), nil
	})
	tool.Label = "Ask User"
	// Malformed questions fail before the user sees them.
	tool.Check = func(_ context.Context, args json.RawMessage) (string, error) {
		var a askUserArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
		return "", validateQuestions(a.Questions)
	}
	return tool
}

const askUserDescription = `Ask the user structured multi-choice questions when you need to clarify intent, validate assumptions, or pick between approaches.

Conventions:
- Provide 2-4 options per question; the host automatically appends a "Type your own answer" entry unless you set "custom": false. Do NOT add "Other" or catch-all options yourself.
- Set "multiSelect": true when answers are not mutually exclusive.
- If you recommend a specific option, put it first and suffix the label with "(Recommended)".
- Question texts must be unique across the call; option labels must be unique within each question.`

func askUserSchema() map[string]any {
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

func validateQuestions(questions []interact.Question) error {
	if len(questions) == 0 {
		return errors.New("at least one question is required")
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

// formatAnswers marks unanswered questions on cancel, so the model still gets
// the partial answers.
func formatAnswers(questions []interact.Question, resp interact.Answers) string {
	parts := make([]string, 0, len(questions))
	anyAnswered := false
	for _, q := range questions {
		answers := resp.Selected[q.Question]
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

func pickPreview(q interact.Question, answers []string) string {
	for _, a := range answers {
		for _, opt := range q.Options {
			if opt.Label == a && opt.Preview != "" {
				return opt.Preview
			}
		}
	}
	return ""
}
