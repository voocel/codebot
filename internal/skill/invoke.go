package skill

import (
	"context"
	"errors"
)

var (
	ErrNotFound              = errors.New("skill not found")
	ErrModelInvocationDenied = errors.New("skill cannot be invoked by the model")
	ErrUserInvocationDenied  = errors.New("skill cannot be invoked by the user")
)

type InvokeInput struct {
	Name      string
	Args      string
	SessionID string
	Source    InvocationSource
}

func ProcessInvocation(ctx context.Context, catalog *Catalog, in InvokeInput) (*InvocationResult, error) {
	if catalog == nil {
		return nil, ErrNotFound
	}
	spec, ok := catalog.Get(in.Name)
	if !ok {
		return nil, ErrNotFound
	}

	switch in.Source {
	case SourceModel:
		if spec.DisableModelInvocation {
			return nil, ErrModelInvocationDenied
		}
	case SourceUser:
		if spec.DisableUserInvocation {
			return nil, ErrUserInvocationDenied
		}
	}

	promptText, err := spec.GetPrompt(ctx, in.Args, in.SessionID)
	if err != nil {
		return nil, err
	}

	mode := ModeInline
	if spec.Context == "fork" {
		mode = ModeFork
	}

	allowedTools := append([]string(nil), spec.AllowedTools...)
	modelOverride := spec.Model
	effort := spec.Effort
	paths := append([]string(nil), spec.Paths...)
	if !SourceAllowsPrivilegedFields(spec.Source) {
		allowedTools = nil
		modelOverride = ""
		effort = ""
		paths = nil
	}

	return &InvocationResult{
		Spec:       spec,
		Mode:       mode,
		PromptText: promptText,
		Agent:      NormalizeAgentType(spec.Agent),
		Delta: Delta{
			AllowedTools:  allowedTools,
			ModelOverride: modelOverride,
			Effort:        effort,
			Paths:         paths,
		},
	}, nil
}
