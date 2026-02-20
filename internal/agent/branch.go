package agent

import (
	"context"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this format:

## Goal
What was the user trying to accomplish?

## Progress
- What was completed
- What is still in progress (if any)

## Key Decisions
- Important decisions made and their rationale (if any)

## Next Steps
What should happen next to continue this work (if applicable)

Keep each section concise (1-3 lines). Preserve exact file paths, function names, and error messages.
If a section has no content, omit it entirely.`

// generateBranchSummary calls the LLM to produce a short summary of the conversation.
// Returns empty string on failure (non-fatal).
func (s *AgentSession) generateBranchSummary(ctx context.Context, msgs []agentcore.AgentMessage) (string, error) {
	s.mu.Lock()
	prov := s.provider
	model := s.modelName
	apiKey := s.apiKey
	baseURL := s.baseURL
	s.mu.Unlock()

	chatModel, err := s.createModel(prov, model, apiKey, baseURL)
	if err != nil {
		return "", err
	}

	// Build a minimal message list: keep the conversation + append summary request.
	var llmMsgs []agentcore.Message
	for _, m := range msgs {
		if msg, ok := m.(agentcore.Message); ok {
			llmMsgs = append(llmMsgs, msg)
		}
	}
	llmMsgs = append(llmMsgs, agentcore.UserMsg(branchSummaryPrompt))

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := chatModel.Generate(ctx, llmMsgs, nil)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Message.TextContent() == "" {
		return "", nil
	}
	return strings.TrimSpace(resp.Message.TextContent()), nil
}
