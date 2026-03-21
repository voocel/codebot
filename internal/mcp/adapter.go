package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/mcp-sdk-go/protocol"
)

// MCPTool adapts an MCP server tool to the agentcore.Tool interface.
type MCPTool struct {
	client   *Client
	tool     protocol.Tool
	fullName string // mcp__<server>__<tool>
}

// NewMCPTool creates an adapter from an MCP tool definition.
func NewMCPTool(client *Client, tool protocol.Tool) *MCPTool {
	return &MCPTool{
		client:   client,
		tool:     tool,
		fullName: "mcp__" + client.Name() + "__" + tool.Name,
	}
}

func (t *MCPTool) Name() string { return t.fullName }

func (t *MCPTool) Label() string {
	if t.tool.Title != "" {
		return t.tool.Title
	}
	return t.tool.Name
}

func (t *MCPTool) Description() string { return t.tool.Description }

func (t *MCPTool) ApprovalMetadata() approval.ToolMetadata {
	return approval.ToolMetadata{
		ToolName:    t.fullName,
		Capability:  t.capability(),
		SummaryHint: t.Label(),
		Reason:      t.reason(),
		KeyPrefix:   "mcp",
	}
}

// Schema converts the MCP tool's InputSchema to the map[string]any format
// expected by agentcore.
func (t *MCPTool) Schema() map[string]any {
	if len(t.tool.InputSchema) == 0 {
		return map[string]any{"type": "object"}
	}
	return t.tool.InputSchema
}

// Execute calls the MCP tool and converts the result to JSON.
func (t *MCPTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var argsMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}

	result, err := t.client.CallTool(ctx, t.tool.Name, argsMap)
	if err != nil {
		return nil, err
	}

	text := extractText(result)
	if result.IsError {
		return nil, fmt.Errorf("tool error: %s", text)
	}
	return json.Marshal(text)
}

// extractText concatenates all TextContent from a CallToolResult.
func extractText(result *protocol.CallToolResult) string {
	var sb strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(protocol.TextContent); ok {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func (t *MCPTool) capability() approval.Capability {
	if ann := t.tool.Annotations; ann != nil {
		switch {
		case ann.DestructiveHint:
			return approval.CapWrite
		case ann.OpenWorldHint:
			return approval.CapNetwork
		case ann.ReadOnlyHint:
			return approval.CapRead
		}
	}

	name := strings.ToLower(t.tool.Name)
	desc := strings.ToLower(strings.TrimSpace(t.tool.Description))
	text := name + " " + desc

	switch {
	case containsAny(text, "search", "fetch", "browse", "http", "https", "url", "web", "remote", "api"):
		return approval.CapNetwork
	case containsAny(text, "create", "update", "write", "edit", "delete", "remove", "send", "post", "publish", "insert", "save"):
		return approval.CapWrite
	case containsAny(text, "read", "list", "get", "show", "find", "query", "inspect"):
		return approval.CapRead
	default:
		return approval.CapUnknown
	}
}

func (t *MCPTool) reason() string {
	switch t.capability() {
	case approval.CapRead:
		return ""
	case approval.CapWrite:
		return "MCP tool may modify external state"
	case approval.CapNetwork:
		return "MCP tool may access external systems"
	default:
		return "MCP tool requires approval"
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
