package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/interact"
)

const askArgs = `{"questions":[{"question":"Which DB?","header":"DB","options":[{"label":"Postgres","description":"relational"},{"label":"Redis","description":"kv"}]}]}`

type fakeUI struct {
	answers interact.Answers
	askErr  error
	asked   []interact.Question
}

func (f *fakeUI) Ask(_ context.Context, qs []interact.Question) (interact.Answers, error) {
	f.asked = qs
	return f.answers, f.askErr
}

func (f *fakeUI) Approve(context.Context, interact.Approval) (interact.Verdict, error) {
	return interact.Verdict{Choice: interact.Deny}, nil
}

// Answers, cancellation and the absence of a user all reach the model as
// text.
func TestAskUserReportsAnswers(t *testing.T) {
	for _, tc := range []struct {
		ui   *fakeUI
		want string
	}{
		{&fakeUI{answers: interact.Answers{Selected: map[string][]string{"Which DB?": {"Postgres"}}}}, `"Which DB?"="Postgres"`},
		{&fakeUI{answers: interact.Answers{Cancelled: true}}, "cancelled"},
		{&fakeUI{askErr: interact.ErrUnsupported}, "unavailable"},
	} {
		text, err := call(t, NewAskUser(tc.ui), askArgs)
		if err != nil {
			t.Fatal(err)
		}
		if len(tc.ui.asked) != 1 || tc.ui.asked[0].Header != "DB" {
			t.Fatalf("asked %+v", tc.ui.asked)
		}
		if !strings.Contains(text, tc.want) {
			t.Errorf("result = %q, want %q", text, tc.want)
		}
	}
}

func TestAskUserRejectsInvalidQuestionsBeforeAsking(t *testing.T) {
	ui := &fakeUI{}
	_, err := call(t, NewAskUser(ui), `{"questions":[]}`)
	if err == nil || !strings.Contains(err.Error(), "at least one question") || ui.asked != nil {
		t.Fatalf("err = %v, asked = %v", err, ui.asked)
	}
}
