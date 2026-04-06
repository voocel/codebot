package plugin

import (
	"testing"

	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/skill"
)

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
		if got := NormalizeTrust(tc.in); got != tc.want {
			t.Fatalf("NormalizeTrust(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRuntimeSkillSource(t *testing.T) {
	t.Parallel()

	if got := RuntimeSkillSource("project", TrustTrusted); got != "project" {
		t.Fatalf("trusted project skill source = %q", got)
	}
	if got := RuntimeSkillSource("project", TrustUntrusted); got != "remote" {
		t.Fatalf("untrusted project skill source = %q", got)
	}
	if IsTrusted("bogus") {
		t.Fatal("expected invalid trust value to be treated as untrusted")
	}
}

func TestRuntimeSkillSpecs(t *testing.T) {
	t.Parallel()

	specs := RuntimeSkillSpecs("user", TrustUntrusted, []skill.Spec{{Name: "review", Source: "user"}})
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if specs[0].Source != "remote" {
		t.Fatalf("expected remote source, got %q", specs[0].Source)
	}
}

func TestAllowedMCPServers(t *testing.T) {
	t.Parallel()

	servers := map[string]mcpclient.ServerConfig{"x": {Command: "echo"}}
	if got := AllowedMCPServers(TrustUntrusted, servers); got != nil {
		t.Fatalf("expected untrusted MCP to be filtered, got %#v", got)
	}
	if got := AllowedMCPServers(TrustTrusted, servers); len(got) != 1 {
		t.Fatalf("expected trusted MCP to pass through, got %#v", got)
	}
}
