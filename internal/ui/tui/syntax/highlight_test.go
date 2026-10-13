package syntax

import "testing"

// A diff highlights its path line by line, so each path is looked up again,
// those no lexer matches too.
func TestFileHighlightsAPathAgain(t *testing.T) {
	for range 2 {
		if got := File("a b", "go.sum"); got != "a b" {
			t.Fatalf("File without a lexer = %q", got)
		}
		if got := File("package x", "x.go"); got == "package x" {
			t.Fatal("File left Go unhighlighted")
		}
	}
}
