package skill

import (
	"context"
	"errors"
	"strings"
)

var (
	ErrNotFound              = errors.New("skill not found")
	ErrModelInvocationDenied = errors.New("skill cannot be invoked by the model")
	ErrUserInvocationDenied  = errors.New("skill cannot be invoked by the user")
)

// Invoker is who invokes a skill.
type Invoker int

const (
	ByModel Invoker = iota
	ByUser
)

type InvokeInput struct {
	Name      string
	Args      string
	SessionID string
	By        Invoker
}

// Invocation is a skill ready to run.
type Invocation struct {
	Spec Spec
	// Prompt is the skill rendered with the invocation's arguments.
	Prompt string
	// Fork runs the skill in a sub-agent, Agent, instead of the conversation.
	Fork  bool
	Agent string
	// AllowedTools and Model are the skill's, if its source is trusted.
	AllowedTools []string
	Model        string
}

// Invoke renders the named skill for an invocation.
func (c *Catalog) Invoke(ctx context.Context, in InvokeInput) (*Invocation, error) {
	spec, ok := c.Get(in.Name)
	if !ok {
		return nil, ErrNotFound
	}
	if in.By == ByModel && spec.DisableModelInvocation {
		return nil, ErrModelInvocationDenied
	}
	if in.By == ByUser && spec.DisableUserInvocation {
		return nil, ErrUserInvocationDenied
	}

	prompt, err := spec.prompt(ctx, in.Args, in.SessionID)
	if err != nil {
		return nil, err
	}
	inv := &Invocation{
		Spec:   spec,
		Prompt: prompt,
		Fork:   spec.Forked(),
		Agent:  strings.TrimSpace(spec.Agent),
	}
	if inv.Agent == "" {
		inv.Agent = "general-purpose"
	}
	if spec.trusted() {
		inv.AllowedTools, inv.Model = spec.AllowedTools, spec.Model
	}
	return inv, nil
}
