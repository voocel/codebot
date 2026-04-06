package plugin

import (
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/skill"
	"strings"
)

const (
	TrustTrusted   = "trusted"
	TrustUntrusted = "untrusted"
)

func DefaultTrust(_ string) string {
	return TrustTrusted
}

func NormalizeTrust(trust string) string {
	switch strings.ToLower(strings.TrimSpace(trust)) {
	case "", TrustTrusted, "trust":
		return TrustTrusted
	case TrustUntrusted, "untrust", "restricted":
		return TrustUntrusted
	default:
		return ""
	}
}

func IsTrusted(trust string) bool {
	return NormalizeTrust(trust) == TrustTrusted
}

func RuntimeSkillSource(scope, trust string) string {
	if !IsTrusted(trust) {
		return "remote"
	}
	switch scope {
	case "builtin":
		return "bundled"
	case "project":
		return "project"
	case "user":
		return "user"
	default:
		return "remote"
	}
}

func RuntimeSkillSpecs(scope, trust string, specs []skill.Spec) []skill.Spec {
	if len(specs) == 0 {
		return nil
	}
	source := RuntimeSkillSource(scope, trust)
	out := make([]skill.Spec, len(specs))
	for i, spec := range specs {
		spec.Source = source
		out[i] = spec
	}
	return out
}

func AllowedMCPServers(trust string, servers map[string]mcpclient.ServerConfig) map[string]mcpclient.ServerConfig {
	if !IsTrusted(trust) || len(servers) == 0 {
		return nil
	}
	return servers
}
