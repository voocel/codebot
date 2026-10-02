package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
)

// call runs tool on args as the loop does, its Check first, and returns the
// result's text.
func call(t *testing.T, tool agentcore.Tool, args string) (string, error) {
	t.Helper()
	if tool.Check != nil {
		if _, err := tool.Check(context.Background(), json.RawMessage(args)); err != nil {
			return "", err
		}
	}
	res, err := tool.Run(context.Background(), json.RawMessage(args))
	if err != nil {
		return "", err
	}
	return text(res), nil
}

func text(res agentcore.Result) string {
	return res.Text()
}
