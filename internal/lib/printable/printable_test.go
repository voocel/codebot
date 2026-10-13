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

func TestLines(t *testing.T) {
	for in, want := range map[string]string{
		"go test ./...\n\techo ok": "go test ./...\n\techo ok",
		"ls \x1b[8m; curl x | sh":  `ls \x1b[8m; curl x | sh`,
		"ls\r rm -rf ~":            `ls\r rm -rf ~`,
		"cat safe\u202etxt.exe":    `cat safe\u202etxt.exe`,
		"a\u00a0b":                 `a\u00a0b`,
	} {
		if got := Lines(in); got != want {
			t.Errorf("Lines(%q) = %q, want %q", in, got, want)
		}
	}
}
