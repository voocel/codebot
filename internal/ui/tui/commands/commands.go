// Package commands holds the slash commands. A command shows what it does
// by sending a transcript.Cell, which joins the conversation, or a
// panel.Panel, which takes the bottom of the screen.
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

// Command is a slash command.
type Command struct {
	Name    string
	Aliases []string
	// Args describes the arguments, "<question>"; "" when it takes none.
	Args        string
	Description string
	// Idle refuses to run while the agent works.
	Idle  bool
	Skill bool
	Run   func(args string) tea.Cmd

	// inline is a skill whose line joins the conversation as the user's
	// input, which shows it.
	inline bool
}

// Copy asks the TUI to put Text on the clipboard.
type Copy struct{ Text string }

// OpenAgent asks the TUI to show the background sub-agent Name.
type OpenAgent struct{ Name string }

// Registry is the slash commands: the built-in ones and one per skill the
// user may invoke.
type Registry struct {
	app      *app.App
	builtins []Command
}

// New returns the commands for a.
func New(a *app.App, version string) *Registry {
	r := &Registry{app: a}
	r.builtins = []Command{
		r.help(),
		model(a),
		compact(a),
		status(a, version),
		contextUsage(a),
		newSession(a),
		resume(a),
		tasks(a),
		agents(a),
		btw(a),
		settings(a),
		mcp(a),
		plugins(a),
		copyReply(a),
		reload(a),
		memory(a),
		undo(a),
		redo(a),
		diff(a),
		worktree(a),
		{Name: "exit", Aliases: []string{"quit", "q"}, Description: "Quit", Run: func(string) tea.Cmd { return tea.Quit }},
	}
	return r
}

// all returns the built-in commands, then those of the skills active now.
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

// Lookup finds a command by name or alias, ignoring case.
func (r *Registry) Lookup(name string) (Command, bool) {
	name = strings.ToLower(name)
	for _, c := range r.all() {
		if c.Name == name || slices.Contains(c.Aliases, name) {
			return c, true
		}
	}
	return Command{}, false
}

// Commands returns the commands to offer: the built-in ones, then the
// skills', each by name.
func (r *Registry) Commands() []Command {
	cmds := r.all()
	byName := func(a, b Command) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(cmds[:len(r.builtins)], byName)
	slices.SortFunc(cmds[len(r.builtins):], byName)
	return cmds
}

// Run runs a "/name args" line. Commands read sessions, run git and wait on
// servers, so they run off the TUI's goroutine.
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

// IsCommand reports whether line is a slash command rather than a message
// starting with a path such as "/usr/bin".
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
			// An inline skill joins the conversation as the user's input; a
			// forked one returns what its sub-agent answered.
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
	{"tab", "Complete a command, take the suggestion"},
	{"pgup / pgdn / wheel", "Scroll the conversation"},
	{"home / end", "Jump to the top or bottom"},
	{"ctrl+o", "Expand or collapse thinking and tool output"},
	{"click", "Expand or collapse one block"},
	{"drag", "Select text and copy it"},
	{"ctrl+t", "Open the transcript in a pager"},
	{"ctrl+v", "Paste an image or text"},
	{"! command", "Run a shell command"},
}

// maxLabel is the widest a table's labels get; a skill's argument hint can
// be any length.
const maxLabel = 24

// table lays rows out as two aligned columns.
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

// info lays out labelled values; a row with an empty value is a section
// heading.
func info(rows [][2]string, width int) []string {
	w := 0
	for _, r := range rows {
		if r[1] != "" {
			w = max(w, ansi.StringWidth(r[0]))
		}
	}
	var out []string
	for _, r := range rows {
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
