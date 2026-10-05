package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/syntax"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// args are a tool call's arguments.
type args map[string]any

// str returns the first of keys that holds a non-empty string.
func (a args) str(keys ...string) string {
	for _, k := range keys {
		if s, ok := a[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// view is how the calls of one tool show: a name, the argument the header
// shows, and the body under it.
type view struct {
	title string // "" for the tool's own name, made readable
	label func(t *Tool) string
	arg   func(a args) string
	body  func(t *Tool, width int, expanded bool) []string
}

func (v view) name(t *Tool) string {
	switch {
	case v.label != nil:
		return v.label(t)
	case v.title != "":
		return v.title
	}
	return humanize(t.Name)
}

// views is set in init: agentBody, among them, reads views itself.
var views map[string]view

func init() {
	views = map[string]view{
		"bash":  {title: "Bash", arg: func(a args) string { return firstLine(a.str("command")) }, body: bashBody},
		"read":  {title: "Read", arg: pathArg, body: readBody},
		"edit":  {title: "Edit", arg: pathArg, body: editBody},
		"write": {title: "Write", arg: pathArg, body: writeBody},
		"grep": {title: "Search", arg: func(a args) string {
			if in := a.str("path", "glob"); in != "" {
				return a.str("pattern") + " in " + ShortPath(in)
			}
			return a.str("pattern")
		}, body: listBody},
		"glob":           {title: "Glob", arg: func(a args) string { return a.str("pattern") }, body: listBody},
		"ls":             {title: "List", arg: pathArg, body: listBody},
		"web_search":     {title: "Web Search", arg: func(a args) string { return a.str("query") }, body: listBody},
		"web_fetch":      {title: "Fetch", arg: func(a args) string { return a.str("url") }, body: fetchBody},
		"subagent":       {label: agentLabel, arg: func(a args) string { return firstLine(a.str("task")) }, body: agentBody},
		"task_output":    {title: "Task Output", arg: func(a args) string { return a.str("task_id") }, body: outputBody},
		"task_stop":      {title: "Stop Task", arg: func(a args) string { return a.str("task_id") }, body: outputBody},
		"tool_search":    {title: "Tool Search", arg: func(a args) string { return a.str("query") }, body: listBody},
		"skill":          {title: "Skill", arg: func(a args) string { return a.str("name", "skill") }, body: outputBody},
		"ask_user":       {title: "Ask", arg: func(args) string { return "" }, body: outputBody},
		"enter_worktree": {title: "Enter Worktree", arg: func(a args) string { return a.str("name") }, body: outputBody},
		"exit_worktree":  {title: "Exit Worktree", arg: func(args) string { return "" }, body: outputBody},
	}
}

func viewOf(tool string) view {
	if v, ok := views[tool]; ok {
		return v
	}
	v := view{arg: anyArg, body: outputBody}
	// An MCP tool, "mcp__server__tool".
	if parts := strings.SplitN(tool, "__", 3); len(parts) == 3 && parts[0] == "mcp" {
		v.title = parts[1] + " · " + humanize(parts[2])
	}
	return v
}

// humanize makes a tool name readable: "task_output" reads "Task Output".
// Title names a tool the way its calls are shown: "Bash", "Web Search";
// a hook command, which goes as the tool "hook/<event>", is a "Hook".
func Title(name string) string {
	if v, ok := views[name]; ok && v.title != "" {
		return v.title
	}
	if strings.HasPrefix(name, "hook/") {
		return "Hook"
	}
	return humanize(name)
}

func humanize(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	if len(words) == 0 {
		return name
	}
	return strings.Join(words, " ")
}

func pathArg(a args) string { return ShortPath(a.str("file_path", "path")) }

// anyArg is the first string argument, by name.
func anyArg(a args) string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := a[k].(string); ok && s != "" {
			return firstLine(s)
		}
	}
	return ""
}

// ShortPath shortens p for display: relative to the working directory when
// inside it, else with ~ for the home directory.
func ShortPath(p string) string {
	if p == "" {
		return ""
	}
	if wd != "" && filepath.IsAbs(p) {
		if rel, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return HomePath(p)
}

// HomePath shows p from the home directory, "~/project", for a directory
// that is the place itself rather than a file in the work.
func HomePath(p string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + p[len(home):]
	}
	return p
}

var (
	wd, _   = os.Getwd()
	home, _ = os.UserHomeDir()
)

// lines splits the text of a result into lines.
func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// output wraps text for a body in st.
func output(text string, width int) []string {
	return markdown.Wrap(text, theme.MutedText, bodyWidth(width))
}

func outputBody(t *Tool, width int, expanded bool) []string {
	if t.State == Running {
		return tail(output(strings.Join(t.output, "\n"), width), 5)
	}
	if strings.TrimSpace(t.Result) == "" {
		return []string{theme.SubtleText.Render("(no output)")}
	}
	if expanded {
		return clip(output(t.Result, width), 400)
	}
	return clip(output(t.Result, width), 8)
}

func errorBody(text string, width int, expanded bool) []string {
	if strings.TrimSpace(text) == "" {
		text = "failed"
	}
	out := markdown.Wrap(text, theme.ErrorText, bodyWidth(width))
	if expanded {
		return clip(out, 200)
	}
	return clip(out, 6)
}

func bashBody(t *Tool, width int, expanded bool) []string {
	if t.State == Running {
		return tail(output(strings.Join(t.output, "\n"), width), 6)
	}
	if strings.TrimSpace(t.Result) == "" {
		return []string{theme.SubtleText.Render("(no output)")}
	}
	if expanded {
		return clip(output(t.Result, width), 400)
	}
	return clip(output(t.Result, width), 10)
}

func readBody(t *Tool, width int, expanded bool) []string {
	if t.State == Running {
		return nil
	}
	n := readCount(t.Result)
	if n == 0 {
		return []string{theme.SubtleText.Render("Read an image or an empty file")}
	}
	if !expanded {
		return []string{theme.MutedText.Render(fmt.Sprintf("Read %d %s", n, plural(n, "line")))}
	}
	return clip(output(t.Result, width), 200)
}

// readCount counts the lines read shows, each after its number and a tab;
// a note on how far the file goes follows them.
func readCount(result string) int {
	n := 0
	for _, l := range lines(result) {
		if num, _, ok := strings.Cut(l, "\t"); ok {
			if _, err := strconv.Atoi(num); err == nil {
				n++
			}
		}
	}
	return n
}

func editBody(t *Tool, width int, expanded bool) []string {
	diff := t.Preview
	if t.State != Running {
		// The result names the file over the diff.
		_, diff, _ = strings.Cut(t.Result, "\n")
	}
	out := renderDiff(diff, t.args.str("file_path", "path"), bodyWidth(width))
	if expanded {
		return out
	}
	return clip(out, 24)
}

// writeBody shows the diff a write awaits approval with, then what it
// wrote: the history keeps the content, not the diff.
func writeBody(t *Tool, width int, expanded bool) []string {
	path := t.args.str("file_path", "path")
	if t.State == Running {
		if t.Preview == "" {
			return nil
		}
		out := renderDiff(t.Preview, path, bodyWidth(width))
		if !expanded {
			out = clip(out, 16)
		}
		return out
	}
	content := strings.TrimSuffix(strings.ReplaceAll(t.args.str("content"), "\t", "    "), "\n")
	if content == "" {
		return []string{theme.SubtleText.Render("Wrote an empty file")}
	}
	code := strings.Split(syntax.File(content, path), "\n")
	digits := len(strconv.Itoa(len(code)))
	out := []string{theme.MutedText.Render(fmt.Sprintf("Wrote %d %s", len(code), plural(len(code), "line")))}
	for i, l := range code {
		out = append(out, fit(theme.FaintText.Render(fmt.Sprintf("%*d ", digits, i+1))+l, bodyWidth(width)))
	}
	if !expanded {
		out = clip(out, 12)
	}
	return out
}

func listBody(t *Tool, width int, expanded bool) []string {
	if t.State == Running {
		return nil
	}
	ls := lines(t.Result)
	if len(ls) == 0 {
		return []string{theme.SubtleText.Render("No results")}
	}
	if expanded {
		return clip(output(t.Result, width), 400)
	}
	return clip(output(t.Result, width), 6)
}

func fetchBody(t *Tool, width int, expanded bool) []string {
	if t.State == Running {
		return nil
	}
	if expanded {
		return clip(output(t.Result, width), 200)
	}
	n := len(lines(t.Result))
	return []string{theme.MutedText.Render(fmt.Sprintf("Fetched %d %s", n, plural(n, "line")))}
}

func agentLabel(t *Tool) string {
	switch {
	case t.args.str("agent") != "":
		return humanize(t.args.str("agent"))
	case t.args["chain"] != nil:
		return "Agent Chain"
	default:
		return "Agents"
	}
}

// agentBody shows the sub-agents' progress while they run, then their
// answer: the history keeps the answer, not the progress.
func agentBody(t *Tool, width int, expanded bool) []string {
	if t.State != Running {
		return outputBody(t, width, expanded)
	}
	var out []string
	for _, a := range t.agents {
		state := theme.WarmText.Render("▸")
		if a.Done {
			state = theme.OKText.Render("✓")
		}
		line := state + " " + theme.Text.Render(a.ID) + theme.SubtleText.Render(fmt.Sprintf(" · %d tools · ↑%s ↓%s", a.Tools, Tokens(a.In), Tokens(a.Out)))
		if !a.Done {
			if act := a.Transcript.activity(); act != "" {
				line += theme.MutedText.Render("  " + act)
			}
		}
		out = append(out, fit(line, bodyWidth(width)))
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
