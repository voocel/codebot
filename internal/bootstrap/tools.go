package bootstrap

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/tools"
)

// ToolFactory creates one tool instance for the given workspace.
type ToolFactory func(cwd string) agentcore.Tool

func defaultToolFactories() []ToolFactory {
	return []ToolFactory{
		func(cwd string) agentcore.Tool { return tools.NewRead(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewWrite(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewEdit(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewBash(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewFind(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewGrep(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewLs(cwd) },
	}
}

func buildTools(cwd string, factories []ToolFactory) []agentcore.Tool {
	if len(factories) == 0 {
		factories = defaultToolFactories()
	}
	out := make([]agentcore.Tool, 0, len(factories))
	for _, factory := range factories {
		if factory == nil {
			continue
		}
		tool := factory(cwd)
		if tool == nil {
			continue
		}
		out = append(out, tool)
	}
	return out
}
