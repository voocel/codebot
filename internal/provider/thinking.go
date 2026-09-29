package provider

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// IsValidThinkingLevel reports whether s is a recognized thinking level.
func IsValidThinkingLevel(s string) bool {
	switch s {
	case "", "off", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func ThinkingLevelsForModel(model agentcore.ChatModel) []string {
	if model == nil {
		return nil
	}
	return thinkingLevelsFromPolicy(llm.ThinkingPolicyFor(model))
}

func ResolveThinkingLevel(model agentcore.ChatModel, level string) (string, bool) {
	if !IsValidThinkingLevel(level) {
		return string(agentcore.ThinkingAuto), false
	}
	if model == nil {
		return level, true
	}
	resolved, ok := llm.ThinkingPolicyFor(model).Resolve(agentcore.ThinkingLevel(level))
	if ok && !IsValidThinkingLevel(string(resolved)) {
		return string(agentcore.ThinkingAuto), false
	}
	return string(resolved), ok
}

func thinkingLevelsFromPolicy(policy llm.ThinkingPolicy) []string {
	if len(policy.Available) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(policy.Available))
	for _, level := range policy.Available {
		value := string(level)
		if IsValidThinkingLevel(value) {
			out = append(out, value)
		}
	}
	return out
}
