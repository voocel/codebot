package ui

import (
	"context"
	"errors"
	"fmt"

	"github.com/voocel/agentcore"
)

// FormatError renders err for user display. Recognizes common cross-cutting
// conditions (cancellation, agent loop limits, LLM provider errors) with
// short friendly messages; falls back to err.Error() for anything else.
//
// fallbackPrefix is prepended when no friendly category matches and the
// fallback is err.Error(). Pass "" to skip.
func FormatError(err error, fallbackPrefix string) string {
	if err == nil {
		return ""
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, agentcore.ErrNoModel):
		return "no model configured; check settings.json"
	case errors.Is(err, agentcore.ErrContextOverflow):
		return "context window full; run /compact or start a new session"
	case errors.Is(err, agentcore.ErrStopGuard):
		return "run terminated by stop guard"
	case errors.Is(err, agentcore.ErrNoMessages):
		return "session history is empty; cannot continue"
	}

	var mte *agentcore.MaxTurnsError
	if errors.As(err, &mte) {
		return fmt.Sprintf("max turns (%d) reached; start a new session or raise MaxTurns", mte.Limit)
	}

	switch {
	case errors.Is(err, agentcore.ErrProviderQuota):
		return "quota exhausted"
	case errors.Is(err, agentcore.ErrProviderRateLimit):
		return "rate limited; retry shortly"
	case errors.Is(err, agentcore.ErrProviderAuth):
		return "API key invalid or expired"
	case errors.Is(err, agentcore.ErrProviderOverloaded):
		return "provider overloaded; retry shortly"
	}

	if fallbackPrefix == "" {
		return err.Error()
	}
	return fallbackPrefix + ": " + err.Error()
}
