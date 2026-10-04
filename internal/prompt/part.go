package prompt

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Part is a part of what the model is told about where it works, besides
// the system prompt. Each is told in a message of its own and told again
// when it changes, so that a change leaves everything told before intact.
type Part struct {
	Key   string // names the part for the whole conversation
	Title string
	Body  string // "" when there is nothing to tell
}

// Text is how the part reads to the model. One with nothing to tell says
// so, which retracts what the part told before.
func (p Part) Text() string {
	body := p.Body
	if body == "" {
		body = "None."
	}
	return "# " + p.Title + "\n\n" + body
}

// Environment tells where the agent runs: the working directory, the OS and
// the date of now.
func Environment(cwd string, now time.Time) Part {
	return Part{Key: "environment", Title: "Environment", Body: fmt.Sprintf(
		"- Working directory: %s\n- OS: %s/%s\n- Today's date: %s",
		cwd, runtime.GOOS, runtime.GOARCH, now.Format("2006-01-02"))}
}

// Skills tells the skills the model may invoke, as listed by skill.Listing.
func Skills(listing string) Part {
	return Part{Key: "skills", Title: "Skills", Body: listing}
}

// Project tells the AGENTS.md files that apply in cwd: ~/.codebot/AGENTS.md,
// then those from the filesystem root down to cwd, CLAUDE.md standing in for
// a directory without one.
func Project(cwd string) Part {
	return Part{Key: "project", Title: "Project Context", Body: loadAgents(cwd)}
}

// MCP tells the instructions the connected MCP servers give.
func MCP(instructions string) Part {
	return Part{Key: "mcp", Title: "MCP Server Instructions", Body: instructions}
}

// DeferredTools tells the tools behind tool_search, whose schemas the model
// has to load before calling them.
func DeferredTools(names []string) Part {
	p := Part{Key: "tools", Title: "Deferred Tools"}
	if len(names) > 0 {
		p.Body = "These tools are available, but their schemas are not loaded: calling one fails until tool_search has loaded it, as with the query \"select:<name>[,<name>...]\".\n\n" +
			strings.Join(names, "\n")
	}
	return p
}
