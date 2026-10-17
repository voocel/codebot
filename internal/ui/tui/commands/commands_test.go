package commands

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/infra/config"
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

func TestModelFacts(t *testing.T) {
	for rate, want := range map[float64]string{3e-6: "$3", 1e-7: "$0.10", 1.25e-6: "$1.25", 7.5e-8: "$0.075", 0: "free"} {
		if got := perMillion(rate); got != want {
			t.Errorf("perMillion(%g) = %q, want %q", rate, got, want)
		}
	}
	for _, c := range []struct {
		name string
		pc   config.ProviderConfig
		want string
	}{
		{"anthropic", config.ProviderConfig{}, ""},
		{"any2", config.ProviderConfig{Type: "anthropic", BaseURL: "https://anyrouter.top"}, "anthropic · anyrouter.top"},
		{"gw", config.ProviderConfig{Type: "openai", API: "responses", BaseURL: "https://x.example/v1"}, "openai · responses API · x.example"},
	} {
		if got := providerNote(c.name, c.pc); got != c.want {
			t.Errorf("providerNote(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}
