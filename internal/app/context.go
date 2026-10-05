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

	"github.com/voocel/codebot/internal/prompt"
	"github.com/voocel/codebot/internal/skill"
)

// A conversation's requests only ever grow: what one sends is the start of
// the next, so the prompt cache serves it and the reasoning Claude keeps
// across turns, which is bound to everything before it, stays valid. The
// system prompt (prompt.System) is built once and never changes while the
// conversation lasts. What does change — the working directory, the date,
// the project's files, git, MCP servers, deferred tools — is told in
// messages, one per prompt.Part, ahead of a run's inputs: each part the
// history does not tell as it now is. Tools are only added, deferred where
// the model defers tools.

// kindContext marks a message telling a part of the context, whose key
// follows.
const kindContext = "context:"

// workspace is what the model is told about the directory the conversation
// works in, loaded when the conversation opens or moves and when the user
// reloads.
func (a *App) workspace(cwd string) []prompt.Part {
	skills := skill.Listing(a.skillCatalog().List(cwd), a.usage.Scores(time.Now()))
	// Memory belongs to the project, not to the worktree the conversation
	// may be in.
	return []prompt.Part{prompt.Skills(skills), prompt.Project(cwd), prompt.Memory(a.cwd), prompt.Git(cwd)}
}

// contextMessages returns the messages telling each part as it is that
// history never told or last told otherwise. A part with nothing to tell is
// told only to retract what history told of it.
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

// deferredNames returns the names of the deferred tools among tools.
func deferredNames(tools []agentcore.Tool) []string {
	var names []string
	for _, t := range tools {
		if t.Deferred {
			names = append(names, t.Name)
		}
	}
	return names
}

// growTools returns kept with fresh's version of each of its tools, then
// the tools of fresh it lacks: a conversation's tools are only added to. A
// tool fresh no longer has stays, and fails when called.
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

// gone runs a tool whose MCP server no longer offers it.
func gone(name string) func(context.Context, json.RawMessage) (agentcore.Result, error) {
	return func(context.Context, json.RawMessage) (agentcore.Result, error) {
		return agentcore.Result{}, fmt.Errorf("%s is no longer available: its MCP server stopped offering it", name)
	}
}

// systemPrompt is the conversation's system prompt, with a cache breakpoint:
// it is the same in every conversation of the workspace.
func (a *App) systemPrompt() []litellm.Block {
	return []litellm.Block{litellm.TextBlock{Text: prompt.System(a.cwd), Cache: a.cache()}}
}

// cache is every cache breakpoint of the conversation's requests. They
// share the TTL: one of a longer TTL may not follow one of a shorter.
func (a *App) cache() *litellm.CacheControl { return &litellm.CacheControl{TTL: a.opts.CacheTTL} }
