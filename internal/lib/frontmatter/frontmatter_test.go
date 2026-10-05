package frontmatter

import "testing"

func TestSplit(t *testing.T) {
	tests := []struct {
		name, in, front, body string
		ok                    bool
	}{
		{"none", "hello", "", "hello", false},
		{"lf", "---\nname: a\n---\nbody\n", "name: a\n", "body\n", true},
		{"crlf", "---\r\nname: a\r\n---\r\nbody", "name: a\r\n", "body", true},
		{"trailing spaces", "--- \nname: a\n---\t\nbody", "name: a\n", "body", true},
		{"empty", "---\n---\nbody", "", "body", true},
		{"closed at the end", "---\nname: a\n---", "name: a\n", "", true},
		{"dashes that are not a delimiter", "---\nnote: ----\n---x\n---\nbody", "note: ----\n---x\n", "body", true},
		{"unclosed", "---\nname: a\nbody", "", "---\nname: a\nbody", false},
		{"not at the start", "\n---\nname: a\n---\n", "", "\n---\nname: a\n---\n", false},
	}
	for _, tt := range tests {
		front, body, ok := Split(tt.in)
		if front != tt.front || body != tt.body || ok != tt.ok {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", tt.name, front, body, ok, tt.front, tt.body, tt.ok)
		}
	}
}
