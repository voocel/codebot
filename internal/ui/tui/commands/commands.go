// Package commands holds the slash commands. A command reports by returning
// a transcript.Cell, which joins the conversation, or a panel.Panel, which
// takes the bottom of the screen.
package commands

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

type Command struct {
	Name    string
	Aliases []string
	// Args is a hint such as "<question>"; "" means no arguments.
	Args        string
	Description string
	// Idle commands refuse to run while the agent works.
	Idle  bool
	Skill bool
	Run   func(args string) tea.Cmd

	// inline marks a skill whose line joins the conversation as user input,
	// so the transcript already shows it.
	inline bool
}

type Copy struct{ Text string }

// OpenAgent opens the page of a background agent's run.
type OpenAgent struct{ Run, Title string }

// Registry holds the built-in commands and one per user-invocable skill.
type Registry struct {
	app      *app.App
	builtins []Command
}

func New(a *app.App, version string) *Registry {
	r := &Registry{app: a}
	r.builtins = []Command{
		r.help(),
		model(a),
		compact(a),
		status(a, version),
		newSession(a),
		resume(a),
		tasks(a),
		btw(a),
		mcp(a),
		plugins(a),
		permissions(a),
		trust(a),
		copyReply(a),
		reload(a),
		memory(a),
		rewind(a),
		worktree(a),
		{Name: "exit", Aliases: []string{"quit", "q"}, Description: "Quit", Run: func(string) tea.Cmd { return tea.Quit }},
	}
	return r
}

func (r *Registry) all() []Command {
	cmds := slices.Clone(r.builtins)
	for _, sk := range r.app.Current().Skills() {
		if sk.DisableUserInvocation || r.builtin(sk.Name) {
			continue
		}
		cmds = append(cmds, Command{
			Name:        sk.Name,
			Args:        sk.ArgumentHint,
			Description: sk.Description,
			Skill:       true,
			inline:      !sk.Forked(),
			Run:         r.skill(sk.Name),
		})
	}
	return cmds
}

func (r *Registry) builtin(name string) bool {
	return slices.ContainsFunc(r.builtins, func(c Command) bool { return c.Name == name })
}

func (r *Registry) Lookup(name string) (Command, bool) {
	name = strings.ToLower(name)
	for _, c := range r.all() {
		if c.Name == name || slices.Contains(c.Aliases, name) {
			return c, true
		}
	}
	return Command{}, false
}

func (r *Registry) Commands() []Command {
	cmds := r.all()
	byName := func(a, b Command) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(cmds[:len(r.builtins)], byName)
	slices.SortFunc(cmds[len(r.builtins):], byName)
	return cmds
}

// Run runs commands off the TUI goroutine because they read sessions, run
// git and wait on servers.
func (r *Registry) Run(line string) tea.Cmd {
	return func() tea.Msg { return tea.BatchMsg{r.run(line)} }
}

func (r *Registry) run(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	echo := emit(&transcript.Prompt{Text: line, Kind: transcript.ToCommand})
	name, args, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	c, ok := r.Lookup(name)
	switch {
	case !ok:
		return tea.Sequence(echo, fail(fmt.Sprintf("Unknown command /%s. Type / to see the commands.", name)))
	case c.Idle && r.app.Current().Status().Running:
		return tea.Sequence(echo, fail("/"+c.Name+" waits for the agent to finish; press esc to stop it."))
	case c.inline:
		return c.Run(strings.TrimSpace(args))
	}
	return tea.Sequence(echo, c.Run(strings.TrimSpace(args)))
}

// IsCommand tells a slash command from a message starting with a path such
// as "/usr/bin".
func IsCommand(line string) bool {
	name, ok := strings.CutPrefix(strings.TrimSpace(line), "/")
	if !ok {
		return false
	}
	name, _, _ = strings.Cut(name, " ")
	return name != "" && !strings.ContainsAny(name, "/\n")
}

func (r *Registry) skill(name string) func(string) tea.Cmd {
	return func(args string) tea.Cmd {
		conv := r.app.Current()
		return func() tea.Msg {
			// An inline skill joins the conversation as user input; a forked
			// one returns its sub-agent's answer.
			out, err := conv.InvokeSkill(context.Background(), name, args)
			switch {
			case err != nil:
				return transcript.Fail("/" + name + ": " + err.Error())
			case out != "":
				return transcript.Print(out)
			}
			return nil
		}
	}
}

func (r *Registry) help() Command {
	return Command{Name: "help", Aliases: []string{"?"}, Description: "Show commands and keys", Run: func(string) tea.Cmd {
		cmds := r.all()
		return show(&panel.Text{Title: "Help", Tabs: []panel.Tab{
			{Name: "commands", Body: func(width int) []string {
				var rows [][2]string
				for _, c := range cmds {
					name := "/" + c.Name
					if c.Args != "" {
						name += " " + c.Args
					}
					desc := c.Description
					if len(c.Aliases) > 0 {
						desc += theme.SubtleText.Render("  /" + strings.Join(c.Aliases, " /"))
					}
					rows = append(rows, [2]string{name, desc})
				}
				return table(rows, width)
			}},
			{Name: "keys", Body: func(width int) []string { return table(keys, width) }},
		}})
	}}
}

var keys = [][2]string{
	{"enter", "Send; while the agent works, steer it"},
	{"ctrl+j / shift+enter", "New line"},
	{"esc", "Stop the agent, close a panel"},
	{"ctrl+c", "Clear the input, stop the agent; twice to quit"},
	{"shift+tab", "Switch the permission mode"},
	{"↑ / ↓", "Recall earlier inputs"},
	{"ctrl+r", "Search earlier inputs"},
	{"ctrl+g", "Write the input in your editor"},
	{"@ path", "Mention a file"},
	{"tab", "Complete a command or a file, take the suggestion"},
	{"pgup / pgdn / wheel", "Scroll the conversation"},
	{"home / end", "Jump to the top or bottom"},
	{"ctrl+o", "Expand or collapse thinking and tool output"},
	{"click", "Expand or collapse one block"},
	{"drag", "Select text and copy it"},
	{"ctrl+t", "Open the transcript in a pager"},
	{"ctrl+v", "Paste an image or text"},
	{"! command", "Run a shell command"},
}

// maxLabel caps label width, since a skill's argument hint can be any
// length.
const maxLabel = 24

func table(rows [][2]string, width int) []string {
	w := 0
	for _, r := range rows {
		w = max(w, ansi.StringWidth(r[0]))
	}
	w = min(w, width/2, maxLabel)
	out := make([]string, len(rows))
	for i, r := range rows {
		label := ansi.Truncate(r[0], w, "…")
		out[i] = ansi.Truncate(theme.AccentText.Render(label)+strings.Repeat(" ", w-ansi.StringWidth(label)+2)+theme.Text.Render(r[1]), width, "…")
	}
	return out
}

// info renders a row with an empty value as a section heading.
func info(rows [][2]string, width int) []string {
	w := 0
	for _, r := range rows {
		if r[1] != "" {
			w = max(w, ansi.StringWidth(r[0]))
		}
	}
	var out []string
	for _, r := range rows {
		r = [2]string{app.Printable(r[0]), app.Printable(r[1])}
		if r[1] == "" {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, theme.Bold.Render(r[0]))
			continue
		}
		out = append(out, ansi.Truncate(theme.MutedText.Render(r[0]+strings.Repeat(" ", w-ansi.StringWidth(r[0])+2))+theme.Text.Render(r[1]), width, "…"))
	}
	return out
}

func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

func note(text string) tea.Cmd   { return emit(transcript.Note(text)) }
func fail(text string) tea.Cmd   { return emit(transcript.Fail(text)) }
func output(text string) tea.Cmd { return emit(transcript.Print(text)) }

func show(p panel.Panel) tea.Cmd { return emit(p) }
