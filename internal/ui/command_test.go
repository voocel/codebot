package ui

import (
	"testing"

	"github.com/voocel/codebot/internal/policy"
)

func TestValidateCommandRequiresIdleWhenRunning(t *testing.T) {
	t.Parallel()

	spec := commandSpec{
		Risk:      policy.RiskLow,
		NeedsIdle: true,
	}
	if err := validateCommand(policy.ProfileBalanced, spec, true); err == nil {
		t.Fatalf("expected running agent command to be denied")
	}
}

func TestValidateCommandAllowsInfoCommandWhenRunning(t *testing.T) {
	t.Parallel()

	spec := commandSpec{
		Risk:      policy.RiskLow,
		NeedsIdle: false,
	}
	if err := validateCommand(policy.ProfileBalanced, spec, true); err != nil {
		t.Fatalf("expected info command to pass while running: %v", err)
	}
}

func TestValidateCommandStillAppliesRiskPolicy(t *testing.T) {
	t.Parallel()

	spec := commandSpec{
		Risk:      policy.RiskMedium,
		NeedsIdle: false,
	}
	if err := validateCommand(policy.ProfileStrict, spec, false); err == nil {
		t.Fatalf("expected strict profile to deny medium risk command")
	}
}
