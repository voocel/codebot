package panel

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/interact"
)

func press(p Panel, keys ...string) (done bool) {
	for _, k := range keys {
		_, done = p.Update(keyPress(k))
	}
	return done
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
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

func fits(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Errorf("%d lines in %d", len(lines), height)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("line %q is %d wide, more than %d", ansi.Strip(l), w, width)
		}
	}
}

func TestPermission(t *testing.T) {
	for _, c := range []struct {
		keys []string
		want interact.Choice
	}{
		{[]string{"enter"}, interact.AllowOnce},
		{[]string{"s"}, interact.AllowSession},
		{[]string{"3"}, interact.AllowAlways},
		{[]string{"down", "down", "down", "enter"}, interact.Deny},
		{[]string{"esc"}, interact.Deny},
	} {
		reply := make(chan interact.Choice, 1)
		p := NewPermission(interact.Approval{Tool: "bash", Summary: "go test ./..."}, reply)
		if !press(p, c.keys...) {
			t.Errorf("%v: the panel stayed", c.keys)
			continue
		}
		if got := <-reply; got != c.want {
			t.Errorf("%v: answered %v, want %v", c.keys, got, c.want)
		}
	}
}

func TestPermissionOnceOnly(t *testing.T) {
	reply := make(chan interact.Choice, 1)
	p := NewPermission(interact.Approval{Tool: "write", OnceOnly: true}, reply)
	view := ansi.Strip(p.View(60, 20))
	if strings.Contains(view, "session") || strings.Contains(view, "always") {
		t.Errorf("a once-only request offers more:\n%s", view)
	}
	if press(p, "s") {
		t.Error("s answered a once-only request")
	}
	press(p, "2")
	if got := <-reply; got != interact.Deny {
		t.Errorf("2 answered %v, want No", got)
	}
}

func TestPermissionKeepsTheChoicesInView(t *testing.T) {
	p := NewPermission(interact.Approval{Tool: "bash", Summary: strings.Repeat("echo line\n", 50)}, make(chan interact.Choice, 1))
	view := p.View(40, 10)
	fits(t, view, 40, 10)
	if !strings.Contains(ansi.Strip(view), "No") {
		t.Errorf("the choices do not show:\n%s", ansi.Strip(view))
	}
}

func TestAskSingle(t *testing.T) {
	reply := make(chan interact.Answers, 1)
	q := interact.Question{Question: "Which?", Options: []interact.Option{{Label: "A"}, {Label: "B"}}}
	a := NewAsk([]interact.Question{q}, reply)
	if !press(a, "down", "enter") {
		t.Fatal("the panel stayed")
	}
	if got := (<-reply).Selected["Which?"]; !slices.Equal(got, []string{"B"}) {
		t.Errorf("answered %v", got)
	}
}

func TestAskCustomAnswer(t *testing.T) {
	reply := make(chan interact.Answers, 1)
	q := interact.Question{Question: "Name?", Options: []interact.Option{{Label: "A"}}}
	a := NewAsk([]interact.Question{q}, reply)
	press(a, "2") // the row for one's own answer
	press(a, "b", "o", "b")
	if !press(a, "enter") {
		t.Fatal("the panel stayed")
	}
	if got := (<-reply).Selected["Name?"]; !slices.Equal(got, []string{"bob"}) {
		t.Errorf("answered %v", got)
	}
}

func TestAskSeveral(t *testing.T) {
	reply := make(chan interact.Answers, 1)
	a := NewAsk([]interact.Question{
		{Question: "Which?", Options: []interact.Option{{Label: "A"}, {Label: "B"}}},
		{Question: "Also?", MultiSelect: true, Options: []interact.Option{{Label: "X"}, {Label: "Y"}, {Label: "Z"}}},
	}, reply)
	press(a, "enter")                 // A, then on to the second
	press(a, "space", "down", "down") // X, then to Z
	press(a, "space", "enter")        // Z, then to the review
	view := ansi.Strip(a.View(60, 20))
	if !strings.Contains(view, "Submit") {
		t.Errorf("no review:\n%s", view)
	}
	if !press(a, "enter") {
		t.Fatal("the panel stayed")
	}
	got := (<-reply).Selected
	if !slices.Equal(got["Which?"], []string{"A"}) || !slices.Equal(got["Also?"], []string{"X", "Z"}) {
		t.Errorf("answered %v", got)
	}
}

func TestAskCancel(t *testing.T) {
	reply := make(chan interact.Answers, 1)
	a := NewAsk([]interact.Question{{Question: "Which?", Options: []interact.Option{{Label: "A"}}}}, reply)
	if !press(a, "esc") || !(<-reply).Cancelled {
		t.Error("esc did not cancel")
	}
}

func TestListFilterAndSelect(t *testing.T) {
	var picked string
	l := &List{
		Title:  "Pick",
		Filter: true,
		Items:  []Item{{Title: "alpha"}, {Title: "beta", Current: true}, {Title: "gamma"}},
		Select: func(it Item) tea.Cmd { picked = it.Title; return nil },
	}
	l.Init()
	if l.selected().Title != "beta" {
		t.Errorf("the list opened on %q, not the current item", l.selected().Title)
	}
	press(l, "g", "a")
	if v := ansi.Strip(l.View(40, 10)); strings.Contains(v, "alpha") || !strings.Contains(v, "gamma") {
		t.Errorf("the filter did not narrow:\n%s", v)
	}
	if !press(l, "enter") || picked != "gamma" {
		t.Errorf("picked %q", picked)
	}

	// esc clears the filter first, then closes.
	press(l, "z")
	if press(l, "esc") {
		t.Error("esc closed the list instead of clearing the filter")
	}
	if !press(l, "esc") {
		t.Error("esc did not close the list")
	}
}

func TestListFits(t *testing.T) {
	var items []Item
	for i := range 40 {
		items = append(items, Item{Title: strings.Repeat("x", i), Detail: "detail", Group: []string{"one", "two"}[i/20]})
	}
	l := &List{Title: "Many", Items: items}
	l.Init()
	press(l, "down", "down", "down")
	fits(t, l.View(30, 12), 30, 12)
}

func TestTextTabs(t *testing.T) {
	tx := &Text{Title: "Info", Tabs: []Tab{
		{Name: "one", Body: Lines("first")},
		{Name: "two", Body: Lines("second")},
	}}
	if v := ansi.Strip(tx.View(40, 10)); !strings.Contains(v, "first") {
		t.Errorf("first tab:\n%s", v)
	}
	press(tx, "tab")
	if v := ansi.Strip(tx.View(40, 10)); !strings.Contains(v, "second") {
		t.Errorf("second tab:\n%s", v)
	}
	if !press(tx, "esc") {
		t.Error("esc did not close")
	}
}

func TestListGrowsAfterScrolling(t *testing.T) {
	var items []Item
	for i := range 30 {
		items = append(items, Item{Title: strings.Repeat("x", i+1)})
	}
	l := &List{Title: "Many", Items: items}
	l.Init()
	l.View(80, 8)
	for range 29 {
		press(l, "down")
	}
	l.View(80, 8)
	fits(t, l.View(80, 14), 80, 14) // the panel grows with the terminal

	// Reloaded shorter under the cursor.
	l.Items = items[:3]
	if v := ansi.Strip(l.View(80, 8)); !strings.Contains(v, "xxx") {
		t.Errorf("after the reload:\n%s", v)
	}
}

func TestAskKeepsTheOptionsInView(t *testing.T) {
	q := interact.Question{Question: strings.Repeat("A long paragraph.\n\n", 10), Options: []interact.Option{{Label: "Yes"}, {Label: "No"}}}
	a := NewAsk([]interact.Question{q}, make(chan interact.Answers, 1))
	view := a.View(80, 10)
	fits(t, view, 80, 10)
	if v := ansi.Strip(view); !strings.Contains(v, "Yes") || !strings.Contains(v, "No") {
		t.Errorf("the options do not show:\n%s", v)
	}
}
