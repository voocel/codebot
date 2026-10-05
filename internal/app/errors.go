package app

import (
	"context"
	"errors"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// hints say what to do about the provider failures a user can act on.
var hints = map[litellm.ErrorType]string{
	litellm.ErrorTypeContextOverflow: "context window full; run /compact or start a new session",
	litellm.ErrorTypeQuota:           "quota exhausted",
	litellm.ErrorTypeRateLimit:       "rate limited; retry shortly",
	litellm.ErrorTypeAuth:            "API key invalid or expired",
	litellm.ErrorTypeOverloaded:      "provider overloaded; retry shortly",
}

// ErrorText is how err reads to the user. A provider failure the user can act
// on says what to do; another a provider reported reads as its own message,
// without the vendor's error code. Whatever err adds around the provider's
// error stays.
func ErrorText(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, agentcore.ErrMaxTurns):
		return "max turns reached; start a new session or raise max_turns"
	}
	var e *litellm.Error
	if !errors.As(err, &e) {
		return err.Error()
	}
	text := e.Error()
	if hint, ok := hints[e.Type]; ok {
		text = hint
		if e.Provider != "" {
			text = e.Provider + ": " + hint
		}
	} else if strings.TrimSpace(e.Message) != "" {
		plain := *e
		plain.Code = ""
		text = plain.Error()
	}
	return strings.Replace(err.Error(), e.Error(), text, 1)
}
