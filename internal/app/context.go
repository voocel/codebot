package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/prompt"
	"github.com/voocel/codebot/internal/agent/skill"
)

// A conversation's requests only grow: each request is a prefix of the next,
// so the prompt cache hits and the reasoning Claude carries across turns
// stays valid. The system prompt is built once per conversation. What changes
// (cwd, date, project files, git, MCP servers, deferred tools) is sent as one
// message per prompt.Part before a run's inputs, only when it differs from
// what the history last said. Tools are only ever added.

// kindContext prefixes a context message's Kind; the part's key follows.
const kindContext = "context:"

// workspace returns the skills active in cwd and the parts describing it.
// The user and the model are offered the same skills the model is told of.
func (a *App) workspace(cwd string) (*skill.Catalog, []prompt.Part) {
	skills := a.skillCatalog().Active(cwd)
	listing := skill.Listing(skills.List(), a.usage.Scores(time.Now()))
	// Memory belongs to the project, not to a worktree.
	return skills, []prompt.Part{prompt.Skills(listing), prompt.Project(cwd), prompt.Memory(a.cwd), prompt.Git(cwd)}
}

// contextMessages returns a message for each part that differs from what
// the history last said about it. An empty part is sent only to retract an
// earlier one.
func contextMessages(parts []prompt.Part, history []agentcore.Message) []agentcore.Message {
	told := map[string]string{}
	for _, m := range history {
		if key, ok := strings.CutPrefix(m.Kind, kindContext); ok {
			told[key] = m.Text()
		}
	}
	var out []agentcore.Message
	for _, p := range parts {
		prev, ok := told[p.Key]
		text := reminder(p.Text())
		if !ok && p.Body == "" || ok && prev == text {
			continue
		}
		m := agentcore.UserText(text)
		m.Kind = kindContext + p.Key
		m.Time = time.Now()
		out = append(out, m)
	}
	return out
}

func deferredNames(tools []agentcore.Tool) []string {
	var names []string
	for _, t := range tools {
		if t.Deferred {
			names = append(names, t.Name)
		}
	}
	return names
}

// growTools never removes a tool, so the request prefix stays the same. A
// tool that fresh no longer has stays and fails when called.
func growTools(kept, fresh []agentcore.Tool) []agentcore.Tool {
	out := make([]agentcore.Tool, 0, len(kept)+len(fresh))
	for _, t := range kept {
		if i := slices.IndexFunc(fresh, func(f agentcore.Tool) bool { return f.Name == t.Name }); i >= 0 {
			t = fresh[i]
		} else {
			t.Run = gone(t.Name)
		}
		out = append(out, t)
	}
	for _, t := range fresh {
		if !slices.ContainsFunc(kept, func(k agentcore.Tool) bool { return k.Name == t.Name }) {
			out = append(out, t)
		}
	}
	return out
}

func gone(name string) func(context.Context, json.RawMessage) (agentcore.Result, error) {
	return func(context.Context, json.RawMessage) (agentcore.Result, error) {
		return agentcore.Result{}, fmt.Errorf("%s is no longer available: its MCP server stopped offering it", name)
	}
}

// systemPrompt has a cache breakpoint because it is the same for every
// conversation in the workspace.
func (a *App) systemPrompt() []litellm.Block {
	return []litellm.Block{litellm.TextBlock{Text: prompt.System(a.cwd), Cache: a.cache()}}
}

// cache is used for every cache breakpoint, so they share one TTL: a
// breakpoint with a longer TTL may not follow one with a shorter TTL.
func (a *App) cache() *litellm.CacheControl { return &litellm.CacheControl{TTL: a.opts.CacheTTL} }
