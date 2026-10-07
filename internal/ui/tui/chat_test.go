package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

type cell struct{ lines []string }

func (c *cell) Render(transcript.Params) []string { return slices.Clone(c.lines) }
func (c *cell) Version() uint64                   { return 0 }
func (c *cell) Live() bool                        { return false }

// chat returns a view of n three-line cells ("c<i>.<j>") and a function
// that adds one.
func chat(n, height int) (*chatView, func()) {
	var cells []transcript.Cell
	add := func() {
		i := len(cells)
		cells = append(cells, &cell{[]string{fmt.Sprintf("c%d.0", i), fmt.Sprintf("c%d.1", i), fmt.Sprintf("c%d.2", i)}})
	}
	for range n {
		add()
	}
	v := newChatView(func() []transcript.Cell { return cells }, emptyPage)
	v.width, v.height = 40, height
	return v, add
}

func TestChatHoldsItsPlaceWhenScrolledBack(t *testing.T) {
	v, add := chat(5, 6)
	if got := v.view(); got[len(got)-1] != "c4.2" || len(got) != 6 {
		t.Fatalf("view = %q", got)
	}
	add()
	if got := v.view(); got[len(got)-1] != "c5.2" {
		t.Errorf("a new cell did not show: %q", got)
	}
	v.scroll(-5)
	before := v.view()
	if v.follow {
		t.Fatal("still following after scrolling back")
	}
	add()
	add()
	if got := v.view(); !slices.Equal(got, before) {
		t.Errorf("the view moved: %q, was %q", got, before)
	}
	if n := v.unseen(); n != 2 {
		t.Errorf("unseen = %d, want 2", n)
	}
	v.scroll(100)
	if !v.follow || v.unseen() != 0 {
		t.Errorf("scrolling to the end: follow %v, unseen %d", v.follow, v.unseen())
	}
}

func TestChatScrollStopsAtTheTop(t *testing.T) {
	v, _ := chat(5, 6)
	v.view()
	v.scroll(-100)
	if got := v.view(); got[0] != "c0.0" {
		t.Errorf("top = %q", got[0])
	}
	v.scroll(1)
	if got := v.view(); got[0] != "c0.1" {
		t.Errorf("one down = %q", got[0])
	}
	v.toBottom()
	if got := v.view(); got[len(got)-1] != "c4.2" {
		t.Errorf("bottom = %q", got)
	}

	// Content shorter than the view does not scroll at all.
	v, _ = chat(1, 10)
	v.scroll(-3)
	if !v.follow {
		t.Error("content shorter than the view scrolled")
	}
	if got := v.view(); len(got) != 3 {
		t.Errorf("view = %q", got)
	}
}

func TestSelectionCopiesTheText(t *testing.T) {
	cells := []transcript.Cell{
		&cell{[]string{"● The answer is", "  in two lines"}},
		&cell{[]string{"● Read(main.go)", "  ⎿ Read 3 lines"}},
	}
	v := newChatView(func() []transcript.Cell { return cells }, emptyPage)
	v.width, v.height = 40, 10
	v.view()

	// From "answer" to the end of the first cell.
	v.press(6, 0)
	v.drag(20, 1)
	if got := v.release(); got != "answer is\nin two lines" {
		t.Errorf("selected %q", got)
	}
	if !v.sel.on {
		t.Error("the selection did not stay")
	}

	// Across cells, the gap and the marks.
	v.press(0, 0)
	v.drag(40, 4)
	want := "The answer is\nin two lines\n\nRead(main.go)\n  Read 3 lines"
	if got := v.release(); got != want {
		t.Errorf("selected %q, want %q", got, want)
	}

	// A click selects nothing.
	v.press(3, 1)
	if got := v.release(); got != "" || v.sel.on {
		t.Errorf("a click selected %q", got)
	}
}

func TestTidy(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"● hello"}, []string{"hello"}},
		{[]string{"● Two:", "  • one", "    ◦ two"}, []string{"Two:", "- one", "  - two"}},
		{[]string{"│ func main() {", "│     x()", "│ }"}, []string{"func main() {", "    x()", "}"}},
	} {
		if got := tidy(c.in, false); got != strings.Join(c.want, "\n") {
			t.Errorf("tidy(%q) = %q", c.in, got)
		}
	}
}

func TestChatSettlesWhenACellShrinks(t *testing.T) {
	long := &cell{}
	for i := range 300 {
		long.lines = append(long.lines, fmt.Sprintf("l%d", i))
	}
	cells := []transcript.Cell{&cell{[]string{"before"}}, long}
	for i := range 10 {
		cells = append(cells, &cell{[]string{fmt.Sprintf("after%d", i)}})
	}
	v := newChatView(func() []transcript.Cell { return cells }, emptyPage)
	v.width, v.height = 40, 5
	v.view()
	v.scroll(-70) // into the long cell, line 250 or so
	long.lines = long.lines[:10]
	v.cache = map[transcript.Cell]*rendered{} // a cell that changes bumps its version

	if got := v.view(); got[0] != "l0" {
		t.Errorf("after the cell shrank the view starts at %q", got[0])
	}
	v.scroll(-2)
	if got := v.view(); got[0] != "before" {
		t.Errorf("two up = %q", got[0])
	}
	v.scroll(3)
	if got := v.view(); got[0] != "l1" || v.follow {
		t.Errorf("three down = %q, follow %v", got[0], v.follow)
	}
}
