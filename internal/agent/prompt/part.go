package prompt

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Part is context outside the system prompt. Each part is sent as its own
// message and resent when it changes, so a change appends to the request
// instead of rewriting earlier messages.
type Part struct {
	Key   string // stable for the whole conversation
	Title string
	Body  string
}

// Text renders an empty part as "None.", which retracts what it said before.
func (p Part) Text() string {
	body := p.Body
	if body == "" {
		body = "None."
	}
	return "# " + p.Title + "\n\n" + body
}

func Environment(cwd string, now time.Time) Part {
	return Part{Key: "environment", Title: "Environment", Body: fmt.Sprintf(
		"- Working directory: %s\n- OS: %s/%s\n- Today's date: %s",
		cwd, runtime.GOOS, runtime.GOARCH, now.Format("2006-01-02"))}
}

func Skills(listing string) Part {
	return Part{Key: "skills", Title: "Skills", Body: listing}
}

func Project(cwd string) Part {
	return Part{Key: "project", Title: "Project Context", Body: loadAgents(cwd)}
}

func MCP(instructions string) Part {
	return Part{Key: "mcp", Title: "MCP Server Instructions", Body: instructions}
}

func DeferredTools(names []string) Part {
	p := Part{Key: "tools", Title: "Deferred Tools"}
	if len(names) > 0 {
		p.Body = "These tools are available, but their schemas are not loaded: calling one fails until tool_search has loaded it, as with the query \"select:<name>[,<name>...]\".\n\n" +
			strings.Join(names, "\n")
	}
	return p
}
