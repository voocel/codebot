package provider

import (
	"slices"

	"github.com/voocel/litellm"
)

// efforts excludes "", which means the provider default.
var efforts = []string{"off", "low", "medium", "high", "xhigh", "max"}

func ValidEffort(effort string) bool {
	return effort == "" || slices.Contains(efforts, effort)
}

// ThinkingLevels returns the efforts a user may pick. reasoning comes from
// the model list and is nil when unknown. "" is always offered; the others
// only if the model may reason and the provider can send that setting.
// Which efforts a reasoning model accepts is left to the vendor.
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

func Thinking(effort string) *litellm.Thinking {
	switch effort {
	case "":
		return nil
	case "off":
		return &litellm.Thinking{Disabled: true}
	}
	return &litellm.Thinking{Effort: effort}
}
