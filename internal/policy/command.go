package policy

import (
	"fmt"
	"strings"
)

// CommandRisk describes slash-command risk level.
type CommandRisk string

const (
	RiskLow    CommandRisk = "low"
	RiskMedium CommandRisk = "medium"
	RiskHigh   CommandRisk = "high"
)

// AllowCommand decides whether a slash command can run under current policy profile.
func AllowCommand(profile Profile, risk CommandRisk, interactive bool) error {
	if profile == "" {
		profile = ProfileBalanced
	}

	r := normalizeRisk(risk)
	if profile == ProfileOff {
		return nil
	}

	switch profile {
	case ProfileStrict:
		if r != RiskLow {
			return fmt.Errorf("policy: command denied in strict profile (risk=%s)", r)
		}
		return nil

	case ProfileBalanced:
		if !interactive {
			if r == RiskLow {
				return nil
			}
			return fmt.Errorf("policy: command denied in non-interactive mode (risk=%s)", r)
		}
		if r == RiskHigh {
			return fmt.Errorf("policy: high-risk command denied")
		}
		return nil

	default:
		return fmt.Errorf("policy: unknown profile %q", profile)
	}
}

func normalizeRisk(risk CommandRisk) CommandRisk {
	switch strings.ToLower(strings.TrimSpace(string(risk))) {
	case string(RiskLow):
		return RiskLow
	case string(RiskHigh):
		return RiskHigh
	default:
		return RiskMedium
	}
}
