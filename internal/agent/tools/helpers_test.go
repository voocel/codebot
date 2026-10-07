package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
)

// call runs Check before the tool, as the loop does.
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
