package plugin

import "testing"

func TestNormalizeTrust(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"", TrustTrusted},
		{"trusted", TrustTrusted},
		{"trust", TrustTrusted},
		{"untrusted", TrustUntrusted},
		{"restricted", TrustUntrusted},
		{"bogus", ""},
	}
	for _, tc := range cases {
		if got := normalizeTrust(tc.in); got != tc.want {
			t.Fatalf("NormalizeTrust(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInvalidTrustIsUntrusted(t *testing.T) {
	t.Parallel()

	if IsTrusted("bogus") {
		t.Fatal("expected invalid trust value to be treated as untrusted")
	}
}
