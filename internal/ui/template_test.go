package ui

import (
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"hello", []string{"hello"}},
		{"a b c", []string{"a", "b", "c"}},
		{`"hello world"`, []string{"hello world"}},
		{`'single quoted'`, []string{"single quoted"}},
		{`a "b c" d`, []string{"a", "b c", "d"}},
		{`  spaced  `, []string{"spaced"}},
		{`"" nonempty`, []string{"nonempty"}},
		{`mixed"quote`, []string{"mixed", "quote"}},
	}
	for _, tt := range tests {
		got := ParseArgs(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("ParseArgs(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseArgs(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestExpand(t *testing.T) {
	tests := []struct {
		name     string
		template string
		args     []string
		want     string
	}{
		{"no placeholders", "hello world", []string{"a"}, "hello world"},
		{"$1 basic", "hello $1", []string{"world"}, "hello world"},
		{"$1 $2", "$1 and $2", []string{"a", "b"}, "a and b"},
		{"$1 out of range", "hello $1", nil, "hello "},
		{"$3 out of range", "$1 $2 $3", []string{"a", "b"}, "a b "},
		{"$@", "args: $@", []string{"x", "y", "z"}, "args: x y z"},
		{"$ARGUMENTS", "args: $ARGUMENTS", []string{"a", "b"}, "args: a b"},
		{"slice ${@:2}", "tail: ${@:2}", []string{"a", "b", "c"}, "tail: b c"},
		{"slice ${@:2:1}", "mid: ${@:2:1}", []string{"a", "b", "c"}, "mid: b"},
		{"slice out of range", "tail: ${@:5}", []string{"a"}, "tail: "},
		{"combined", "$1 does $@ with ${@:2}", []string{"go", "fast", "well"}, "go does go fast well with fast well"},
		{"no args $@", "all: $@", nil, "all: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Expand(tt.template, tt.args)
			if got != tt.want {
				t.Errorf("Expand(%q, %v) = %q, want %q", tt.template, tt.args, got, tt.want)
			}
		})
	}
}
