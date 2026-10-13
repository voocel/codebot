package commands

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A label is aligned as shown, quoted when it is not printable as it is.
func TestInfoAlignsShownLabels(t *testing.T) {
	rows := [][2]string{{`"github`, "on"}, {"a\tb", "off"}, {"plain", "on"}}
	out := info(rows, 80)
	col := -1
	for _, line := range out {
		line = ansi.Strip(line)
		i := strings.LastIndex(line, "  ") + 2
		if col >= 0 && i != col {
			t.Fatalf("values not aligned:\n%s", ansi.Strip(strings.Join(out, "\n")))
		}
		col = i
	}
	if rows[0][0] != `"github` {
		t.Fatal("info changed its rows")
	}
}
