// Package diag provides lightweight error classification for runtime metrics
// and status panels. It deliberately avoids defining a custom Error type:
// callers wrap errors with fmt.Errorf("...: %w", diag.ErrXxx) and use
// errors.Is to detect categories.
package diag

import (
	"context"
	"errors"

	"github.com/voocel/agentcore"
)

// Category classifies errors for metrics and human-readable status output.
// Use Categorize(err) to derive a Category from any error chain.
type Category string

const (
	CatUnknown    Category = "unknown"
	CatCanceled   Category = "canceled"
	CatConfig     Category = "config"
	CatPermission Category = "permission"
	CatProvider   Category = "provider"
	CatSession    Category = "session"
	CatToolInput  Category = "tool_input"
	CatToolExec   Category = "tool_exec"
	CatLLM        Category = "llm"
	CatAgent      Category = "agent"
)

// Sentinel errors that callers wrap with fmt.Errorf("...: %w", diag.ErrXxx)
// to declare a category. Categorize() walks the chain and matches via errors.Is.
var (
	ErrConfig     = errors.New("config error")
	ErrPermission = errors.New("permission denied")
	ErrProvider   = errors.New("provider error")
	ErrSession    = errors.New("session error")
	ErrToolInput  = errors.New("tool input error")
	ErrToolExec   = errors.New("tool execution error")
)

// Categorize walks err's chain and returns the matching Category. More
// specific cases are checked first; unrecognized errors return CatUnknown.
func Categorize(err error) Category {
	if err == nil {
		return CatUnknown
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return CatCanceled
	case errors.Is(err, ErrConfig):
		return CatConfig
	case errors.Is(err, ErrPermission):
		return CatPermission
	case errors.Is(err, ErrProvider):
		return CatProvider
	case errors.Is(err, ErrSession):
		return CatSession
	case errors.Is(err, ErrToolInput), errors.Is(err, agentcore.ErrToolValidation):
		return CatToolInput
	case errors.Is(err, ErrToolExec):
		return CatToolExec
	}

	if isAgentLoop(err) {
		return CatAgent
	}

	if isProviderError(err) {
		return CatLLM
	}
	return CatUnknown
}

func isProviderError(err error) bool {
	classified := agentcore.ClassifyProvider(err)
	return errors.Is(classified, agentcore.ErrProviderRateLimit) ||
		errors.Is(classified, agentcore.ErrProviderQuota) ||
		errors.Is(classified, agentcore.ErrProviderTimeout) ||
		errors.Is(classified, agentcore.ErrProviderStreamIdle) ||
		errors.Is(classified, agentcore.ErrProviderNetwork) ||
		errors.Is(classified, agentcore.ErrProviderAuth) ||
		errors.Is(classified, agentcore.ErrProviderOverloaded)
}

func isAgentLoop(err error) bool {
	return errors.Is(err, agentcore.ErrMaxTurns) ||
		errors.Is(err, agentcore.ErrNoModel) ||
		errors.Is(err, agentcore.ErrNoMessages) ||
		errors.Is(err, agentcore.ErrAlreadyRunning) ||
		errors.Is(err, agentcore.ErrBadContinuation) ||
		errors.Is(err, agentcore.ErrStopGuard) ||
		errors.Is(err, agentcore.ErrContextOverflow) ||
		errors.Is(err, agentcore.ErrStreamPartial)
}
