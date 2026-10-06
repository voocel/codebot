package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/mcp-sdk-go/protocol"

	"github.com/voocel/codebot/internal/agent/permission"
)

// newTool adapts the MCP tool t of server c, naming it mcp__<server>__<tool>.
func newTool(c *Client, t *protocol.Tool) agentcore.Tool {
	schema := t.InputSchema
	if len(schema) == 0 {
		schema = map[string]any{"type": "object"}
	}
	return agentcore.Tool{
		Name:        toolName(c.Name(), t.Name),
		Label:       label(t),
		Description: t.Description,
		Schema:      schema,
		Run: func(ctx context.Context, args json.RawMessage) (agentcore.Result, error) {
			var argsMap map[string]any
			if err := json.Unmarshal(args, &argsMap); err != nil {
				return agentcore.Result{}, fmt.Errorf("invalid arguments: %w", err)
			}
			result, err := c.CallTool(ctx, t.Name, argsMap)
			if err != nil {
				return agentcore.Result{}, err
			}
			text := extractText(result)
			if result.IsError {
				return agentcore.Result{}, fmt.Errorf("tool error: %s", text)
			}
			return agentcore.TextResult(text), nil
		},
	}
}

func label(t *protocol.Tool) string {
	if t.Title != "" {
		return t.Title
	}
	return t.Name
}

// permissionOf is how the permission engine sees the MCP tool t.
func permissionOf(t *protocol.Tool) permission.Metadata {
	capability := capabilityOf(t)
	return permission.Metadata{
		Capability:  capability,
		SummaryHint: label(t),
		Reason:      reasonFor(capability),
		KeyPrefix:   "mcp",
	}
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

func capabilityOf(t *protocol.Tool) permission.Capability {
	if ann := t.Annotations; ann != nil {
		switch {
		case hintEnabled(ann.DestructiveHint):
			return permission.CapabilityWrite
		case hintEnabled(ann.OpenWorldHint):
			return permission.CapabilityNetwork
		case hintEnabled(ann.ReadOnlyHint):
			return permission.CapabilityRead
		}
	}

	name := strings.ToLower(t.Name)
	desc := strings.ToLower(strings.TrimSpace(t.Description))
	text := name + " " + desc

	switch {
	case containsAny(text, "search", "fetch", "browse", "http", "https", "url", "web", "remote", "api"):
		return permission.CapabilityNetwork
	case containsAny(text, "create", "update", "write", "edit", "delete", "remove", "send", "post", "publish", "insert", "save"):
		return permission.CapabilityWrite
	case containsAny(text, "read", "list", "get", "show", "find", "query", "inspect"):
		return permission.CapabilityRead
	default:
		return permission.CapabilityUnknown
	}
}

func hintEnabled(hint *bool) bool {
	return hint != nil && *hint
}

func reasonFor(capability permission.Capability) string {
	switch capability {
	case permission.CapabilityRead:
		return ""
	case permission.CapabilityWrite:
		return "MCP tool may modify external state"
	case permission.CapabilityNetwork:
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

// maxToolName is the longest tool name the vendors take, of letters,
// digits, "_" and "-".
const maxToolName = 64

// toolName names the tool of server: mcp__<server>__<tool>, with what the
// vendors refuse in a tool name made "-", and a name too long cut and told
// apart by a hash of the whole.
func toolName(server, tool string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '-'
	}, "mcp__"+server+"__"+tool)
	if len(name) <= maxToolName {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:maxToolName-9] + "_" + hex.EncodeToString(sum[:4])
}
