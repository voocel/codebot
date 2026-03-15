package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
