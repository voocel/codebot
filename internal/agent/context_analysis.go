package agent

import (
	"fmt"
	"sort"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
)

// ContextBreakdown reports per-category token distribution in the conversation.
type ContextBreakdown struct {
	UserText      int
	AssistantText int
	ToolCalls     int
	ToolResults   int
	Summaries     int
	Images        int
	Total         int
	ContextWindow int

	// TopTools lists the heaviest tools by total (call + result) tokens.
	TopTools []ToolTokenUsage
}

// ToolTokenUsage tracks token consumption for a single tool.
type ToolTokenUsage struct {
	Name         string
	CallTokens   int
	ResultTokens int
	Total        int
}

// ContextSuggestion is an actionable recommendation to reduce context usage.
type ContextSuggestion struct {
	Severity string // "warning" or "info"
	Message  string
	Savings  int // estimated token savings, 0 if unknown
}

// ContextBreakdown computes a per-category token breakdown of the current
// conversation messages. It does NOT include system prompt or tool definitions.
func (s *Session) ContextBreakdown() ContextBreakdown {
	msgs := s.deps.agent.Messages()
	usage := s.deps.agent.ContextUsage()
	window := 0
	if usage != nil {
		window = usage.ContextWindow
	}
	return analyzeBreakdown(msgs, window)
}

// ContextSuggestions generates actionable suggestions based on context usage
// and the message breakdown.
func (s *Session) ContextSuggestions() []ContextSuggestion {
	usage := s.deps.agent.ContextUsage()
	if usage == nil || usage.ContextWindow <= 0 {
		return nil
	}
	bd := s.ContextBreakdown()
	return generateSuggestions(bd, usage)
}

func analyzeBreakdown(msgs []agentcore.AgentMessage, contextWindow int) ContextBreakdown {
	bd := ContextBreakdown{ContextWindow: contextWindow}
	toolNames := make(map[string]string)        // tool_call_id → tool name
	toolMap := make(map[string]*ToolTokenUsage) // tool name → usage

	for _, am := range msgs {
		switch msg := am.(type) {
		case agentctx.ContextSummary:
			bd.Summaries += estimateChars(len(msg.Summary))

		case agentcore.Message:
			for _, b := range msg.Content {
				tokens := estimateBlock(b)
				switch {
				case b.Type == agentcore.ContentToolCall && b.ToolCall != nil:
					bd.ToolCalls += tokens
					toolNames[b.ToolCall.ID] = b.ToolCall.Name
					entry := getOrCreate(toolMap, b.ToolCall.Name)
					entry.CallTokens += tokens

				case b.Type == agentcore.ContentImage:
					bd.Images += tokens

				case msg.Role == agentcore.RoleTool:
					bd.ToolResults += tokens
					if id, _ := msg.Metadata["tool_call_id"].(string); id != "" {
						if name, ok := toolNames[id]; ok {
							entry := getOrCreate(toolMap, name)
							entry.ResultTokens += tokens
						}
					}

				case msg.Role == agentcore.RoleUser:
					bd.UserText += tokens

				case msg.Role == agentcore.RoleAssistant:
					bd.AssistantText += tokens
				}
			}
		}
	}

	bd.Total = bd.UserText + bd.AssistantText + bd.ToolCalls + bd.ToolResults + bd.Summaries + bd.Images

	// Build sorted top tools list
	tools := make([]ToolTokenUsage, 0, len(toolMap))
	for _, t := range toolMap {
		t.Total = t.CallTokens + t.ResultTokens
		tools = append(tools, *t)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Total > tools[j].Total })
	if len(tools) > 5 {
		tools = tools[:5]
	}
	bd.TopTools = tools
	return bd
}

const (
	nearCapacityPercent     = 80
	largeToolResultPercent  = 15
	largeToolResultMinToken = 10_000
	readBloatPercent        = 5
	bashBloatPercent        = 10
)

func generateSuggestions(bd ContextBreakdown, usage *agentcore.ContextUsage) []ContextSuggestion {
	var suggestions []ContextSuggestion
	window := usage.ContextWindow
	if window <= 0 {
		return nil
	}

	// Near capacity warning
	if usage.Percent >= nearCapacityPercent {
		suggestions = append(suggestions, ContextSuggestion{
			Severity: "warning",
			Message:  fmt.Sprintf("Context is %.0f%% full. Consider /compact or start a new conversation.", usage.Percent),
		})
	}

	// Large tool results
	if bd.ToolResults > 0 {
		pct := float64(bd.ToolResults) / float64(window) * 100
		if pct >= largeToolResultPercent && bd.ToolResults >= largeToolResultMinToken {
			suggestions = append(suggestions, ContextSuggestion{
				Severity: "warning",
				Message:  fmt.Sprintf("Tool results consume %.0f%% of context. Large outputs fill context quickly.", pct),
				Savings:  bd.ToolResults / 2,
			})
		}
	}

	// Per-tool suggestions
	for _, t := range bd.TopTools {
		pct := float64(t.ResultTokens) / float64(window) * 100
		switch t.Name {
		case "bash":
			if pct >= float64(bashBloatPercent) {
				suggestions = append(suggestions, ContextSuggestion{
					Severity: "info",
					Message:  fmt.Sprintf("Bash results use %.0f%% of context. Use head/tail/grep to limit output.", pct),
					Savings:  t.ResultTokens / 2,
				})
			}
		case "read":
			if pct >= float64(readBloatPercent) {
				suggestions = append(suggestions, ContextSuggestion{
					Severity: "info",
					Message:  fmt.Sprintf("Read results use %.0f%% of context. Use offset/limit for large files.", pct),
					Savings:  t.ResultTokens * 3 / 10,
				})
			}
		case "grep":
			if pct >= float64(readBloatPercent) {
				suggestions = append(suggestions, ContextSuggestion{
					Severity: "info",
					Message:  fmt.Sprintf("Grep results use %.0f%% of context. Use stricter patterns or type filters.", pct),
					Savings:  t.ResultTokens * 3 / 10,
				})
			}
		case "web_fetch":
			if pct >= float64(readBloatPercent) {
				suggestions = append(suggestions, ContextSuggestion{
					Severity: "info",
					Message:  fmt.Sprintf("WebFetch results use %.0f%% of context. Extract only needed content.", pct),
					Savings:  t.ResultTokens * 4 / 10,
				})
			}
		}
	}

	return suggestions
}

func estimateBlock(b agentcore.ContentBlock) int {
	switch b.Type {
	case agentcore.ContentText:
		return estimateChars(len(b.Text))
	case agentcore.ContentThinking:
		return estimateChars(len(b.Thinking))
	case agentcore.ContentToolCall:
		if b.ToolCall != nil {
			return estimateChars(len(b.ToolCall.Name) + len(b.ToolCall.Args))
		}
		return 1
	case agentcore.ContentImage:
		return 1200
	default:
		return 1
	}
}

func estimateChars(n int) int {
	if n <= 0 {
		return 1
	}
	return (n + 3) / 4
}

func getOrCreate(m map[string]*ToolTokenUsage, name string) *ToolTokenUsage {
	if e, ok := m[name]; ok {
		return e
	}
	e := &ToolTokenUsage{Name: name}
	m[name] = e
	return e
}
