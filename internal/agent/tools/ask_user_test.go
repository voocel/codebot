package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/interact"
)

const askArgs = `{"questions":[{"question":"Which DB?","header":"DB","options":[{"label":"Postgres","description":"relational"},{"label":"Redis","description":"kv"}]}]}`

// fakeUI answers with fixed responses and records what it was shown.
type fakeUI struct {
	answers interact.Answers
	askErr  error
	asked   []interact.Question
}

func (f *fakeUI) Ask(_ context.Context, qs []interact.Question) (interact.Answers, error) {
	f.asked = qs
	return f.answers, f.askErr
}

func (f *fakeUI) Approve(context.Context, interact.Approval) (interact.Choice, error) {
	return interact.Deny, nil
}

func runAskUser(t *testing.T, ui *fakeUI, args string) string {
	t.Helper()
	text, err := call(t, NewAskUser(ui), args)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestAskUserReportsAnswers(t *testing.T) {
	ui := &fakeUI{answers: interact.Answers{Selected: map[string][]string{"Which DB?": {"Postgres"}}}}
	text := runAskUser(t, ui, askArgs)
	if len(ui.asked) != 1 || ui.asked[0].Header != "DB" {
		t.Fatalf("asked %+v", ui.asked)
	}
	if !strings.Contains(text, `"Which DB?"="Postgres"`) {
		t.Fatalf("result = %q", text)
	}
}

func TestAskUserReportsCancellation(t *testing.T) {
	text := runAskUser(t, &fakeUI{answers: interact.Answers{Cancelled: true}}, askArgs)
	if !strings.Contains(text, "cancelled") {
		t.Fatalf("result = %q", text)
	}
}

func TestAskUserWithoutAnInteractiveUser(t *testing.T) {
	text := runAskUser(t, &fakeUI{askErr: interact.ErrUnsupported}, askArgs)
	if !strings.Contains(text, "unavailable") {
		t.Fatalf("result = %q", text)
	}
}

func TestAskUserRejectsInvalidQuestionsBeforeAsking(t *testing.T) {
	ui := &fakeUI{}
	_, err := call(t, NewAskUser(ui), `{"questions":[]}`)
	if err == nil || !strings.Contains(err.Error(), "at least one question") || ui.asked != nil {
		t.Fatalf("err = %v, asked = %v", err, ui.asked)
	}
}
