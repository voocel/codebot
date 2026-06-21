package bootstrap

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/tools"
)

// ToolFactory creates one tool instance for the given workspace.
type ToolFactory func(cwd string) agentcore.Tool

// defaultToolFactories binds read / write / edit to a shared FileReadState so
// they can enforce read-before-write and detect stale writes, and to the
// session's WorkspaceFS backend (nil → local filesystem). Other tools do not
// need either.
func defaultToolFactories(state *tools.FileReadState, fs tools.WorkspaceFS) []ToolFactory {
	return []ToolFactory{
		func(cwd string) agentcore.Tool { return tools.NewRead(cwd, state, tools.WithFS(fs)) },
		func(cwd string) agentcore.Tool { return tools.NewWrite(cwd, state, tools.WithFS(fs)) },
		func(cwd string) agentcore.Tool { return tools.NewEdit(cwd, state, tools.WithFS(fs)) },
		func(cwd string) agentcore.Tool { return tools.NewBash(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewGlob(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewGrep(cwd) },
		func(cwd string) agentcore.Tool { return tools.NewLs(cwd) },
	}
}

func buildTools(cwd string, factories []ToolFactory) []agentcore.Tool {
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
