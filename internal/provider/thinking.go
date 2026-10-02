package provider

import (
	"slices"

	"github.com/voocel/litellm"
)

// efforts are the reasoning efforts a user may pick besides "", the provider
// default.
var efforts = []string{"off", "low", "medium", "high", "xhigh", "max"}

// ValidEffort reports whether effort is one a user may pick.
func ValidEffort(effort string) bool {
	return effort == "" || slices.Contains(efforts, effort)
}

// ThinkingLevels lists the reasoning efforts a user may pick for a model of
// client that the model list says reasoning of, nil when it does not know:
// "" always, and the others unless the model does not reason, each where
// the provider sends it. Which efforts a reasoning model takes is the
// vendor's call.
func ThinkingLevels(client *litellm.Client, reasoning *bool) []string {
	levels := []string{""}
	if reasoning != nil && !*reasoning {
		return levels
	}
	caps, known := client.Capabilities()
	for _, effort := range efforts {
		if !known || effort == "off" && caps.DisableThinking || effort != "off" && caps.ThinkingEffort {
			levels = append(levels, effort)
		}
	}
	return levels
}

// Thinking is the request's setting for a reasoning effort; nil leaves the
// provider default.
func Thinking(effort string) *litellm.Thinking {
	switch effort {
	case "":
		return nil
	case "off":
		return &litellm.Thinking{Disabled: true}
	}
	return &litellm.Thinking{Effort: effort}
}
