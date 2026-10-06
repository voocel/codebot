package printable

import "testing"

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"plain text · ✓":    "plain text · ✓",
		`a\nb`:              `a\nb`,
		"a\nb\tc":           `"a\nb\tc"`,
		`"a\nb"`:            `"\"a\\nb\""`,
		"\x1b[2Kgone":       `"\x1b[2Kgone"`,
		"safe\u202etxt.exe": `"safe\u202etxt.exe"`,
	} {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}
