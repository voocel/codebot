package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

func TestErrorText(t *testing.T) {
	rejected := &litellm.Error{
		Type:       litellm.ErrorTypeValidation,
		Code:       "invalid_request_error",
		Message:    "The supported API model names are deepseek-flash, deepseek-v4-pro, but you passed deepseek-v4.1-flash. (request_id: 7ef32fa7)",
		Provider:   "deepseek",
		StatusCode: 400,
	}
	const said = "deepseek: The supported API model names are deepseek-flash, deepseek-v4-pro, but you passed deepseek-v4.1-flash. (request_id: 7ef32fa7) (HTTP 400)"
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"the provider's words, without its code", rejected, said},
		{"what wraps it stays", fmt.Errorf("agentcore: compact: %w", rejected), "agentcore: compact: " + said},
		{"a failure the user can act on", &litellm.Error{Type: litellm.ErrorTypeAuth, Code: "authentication_error", Message: "invalid key", Provider: "deepseek", StatusCode: 401},
			"deepseek: API key invalid or expired; codebot -setup changes it"},
		{"a code alone is kept", &litellm.Error{Type: litellm.ErrorTypeProvider, Code: "server_error", Provider: "deepseek", StatusCode: 500},
			"deepseek: server_error (HTTP 500)"},
		{"max turns", fmt.Errorf("%w (50)", agentcore.ErrMaxTurns), "max turns reached; start a new session or raise max_turns"},
		{"canceled", context.Canceled, "canceled"},
		{"another error", errors.New("disk full"), "disk full"},
	} {
		if got := ErrorText(tt.err); got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}
