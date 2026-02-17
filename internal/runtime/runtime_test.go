package runtime

import (
	"testing"

	"github.com/voocel/codebot/internal/policy"
)

func TestParseProfile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want policy.Profile
	}{
		{in: "", want: policy.ProfileBalanced},
		{in: "balanced", want: policy.ProfileBalanced},
		{in: "strict", want: policy.ProfileStrict},
		{in: "off", want: policy.ProfileOff},
		{in: "  StRiCt  ", want: policy.ProfileStrict},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseProfile(tc.in)
			if err != nil {
				t.Fatalf("parseProfile(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseProfile(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProfileInvalid(t *testing.T) {
	t.Parallel()

	if _, err := parseProfile("unknown"); err == nil {
		t.Fatalf("expected invalid profile error")
	}
}
