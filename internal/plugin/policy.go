package plugin

import "strings"

const (
	TrustTrusted   = "trusted"
	TrustUntrusted = "untrusted"
)

func normalizeTrust(trust string) string {
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
	return normalizeTrust(trust) == TrustTrusted
}
