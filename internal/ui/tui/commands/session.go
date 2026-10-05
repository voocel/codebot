package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

func compact(a *app.App) Command {
	return Command{Name: "compact", Description: "Summarize the conversation to free context", Idle: true, Run: func(string) tea.Cmd {
		conv := a.Current()
		return func() tea.Msg {
			// The transcript reports the compaction from its events.
			if err := conv.Compact(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
				return transcript.Fail("Compaction failed: " + app.ErrorText(err))
			}
			return nil
		}
	}}
}

func newSession(a *app.App) Command {
	return Command{Name: "new", Aliases: []string{"clear"}, Description: "Start a new conversation", Idle: true, Run: func(string) tea.Cmd {
		return open(a, "")
	}}
}

// open opens session id, a new one for "", off the TUI's goroutine: opening
// publishes to it.
func open(a *app.App, id string) tea.Cmd {
	return func() tea.Msg {
		if _, err := a.Open(id); err != nil {
			return transcript.Fail("Could not open the session: " + err.Error())
		}
		return nil
	}
}

func resume(a *app.App) Command {
	return Command{Name: "resume", Aliases: []string{"r"}, Description: "Resume an earlier conversation", Idle: true, Run: func(string) tea.Cmd {
		sessions, err := a.Sessions()
		if err != nil {
			return fail("Could not list the sessions: " + err.Error())
		}
		current := a.Current().ID()
		var items []panel.Item
		for _, s := range sessions {
			title := strings.TrimSpace(s.FirstMessage)
			if title == "" {
				title = "(empty)"
			}
			items = append(items, panel.Item{
				Title:   title,
				Detail:  fmt.Sprintf("%s · %d messages", age(time.Since(s.Updated)), s.MessageCount),
				Current: s.ID == current,
				Value:   s.ID,
			})
		}
		if len(items) == 0 {
			return note("No conversations yet.")
		}
		return show(&panel.List{Title: "Resume", Items: items, Filter: true, Select: func(it panel.Item) tea.Cmd {
			if it.Current {
				return nil
			}
			return open(a, it.Value.(string))
		}})
	}}
}

func copyReply(a *app.App) Command {
	return Command{Name: "copy", Description: "Copy the last reply", Run: func(string) tea.Cmd {
		for _, m := range slices.Backward(a.Current().History()) {
			if m.Role != litellm.RoleAssistant {
				continue
			}
			if text := strings.TrimSpace(m.Text()); text != "" {
				return emit(Copy{text})
			}
		}
		return note("No reply to copy yet.")
	}}
}

func undo(a *app.App) Command {
	return Command{Name: "undo", Description: "Revert the files the last turn changed", Idle: true, Run: func(string) tea.Cmd {
		changed, ok, err := a.Current().Undo()
		return reverted("Reverted", changed, ok, err, "Nothing to undo.")
	}}
}

func redo(a *app.App) Command {
	return Command{Name: "redo", Description: "Re-apply what /undo reverted", Idle: true, Run: func(string) tea.Cmd {
		changed, ok, err := a.Current().Redo()
		return reverted("Restored", changed, ok, err, "Nothing to redo.")
	}}
}

func reverted(verb string, changed []string, ok bool, err error, none string) tea.Cmd {
	switch {
	case err != nil:
		return fail(err.Error())
	case !ok:
		return note(none)
	case len(changed) == 0:
		return note("The turn changed no files.")
	}
	return output(fmt.Sprintf("%s %d %s:\n  %s", verb, len(changed), plural(len(changed), "file"), strings.Join(changed, "\n  ")))
}

func diff(a *app.App) Command {
	return Command{Name: "diff", Description: "Show the files /undo would revert", Idle: true, Run: func(string) tea.Cmd {
		numstat, err := a.Current().Diff()
		if err != nil {
			return fail(err.Error())
		}
		var rows []string
		var adds, dels int
		for line := range strings.SplitSeq(strings.TrimSpace(numstat), "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) != 3 {
				continue
			}
			if f[0] == "-" {
				rows = append(rows, theme.SubtleText.Render("binary  ")+theme.PathText.Render(f[2]))
				continue
			}
			add, _ := strconv.Atoi(f[0])
			del, _ := strconv.Atoi(f[1])
			adds, dels = adds+add, dels+del
			rows = append(rows, theme.OKText.Render(fmt.Sprintf("+%-4d", add))+theme.ErrorText.Render(fmt.Sprintf("-%-4d", del))+theme.PathText.Render(f[2]))
		}
		if len(rows) == 0 {
			return note("No changes to undo.")
		}
		head := theme.MutedText.Render(fmt.Sprintf("%d %s · +%d -%d · /undo reverts them", len(rows), plural(len(rows), "file"), adds, dels))
		return show(&panel.Text{Title: "Last turn's changes", Tabs: []panel.Tab{{Body: panel.Lines(append([]string{head, ""}, rows...)...)}}})
	}}
}

func contextUsage(a *app.App) Command {
	return Command{Name: "context", Description: "Show how much context the conversation uses", Run: func(string) tea.Cmd {
		conv := a.Current()
		st := conv.Status()
		var messages, summaries int
		for _, m := range conv.History() {
			messages++
			if m.Kind == agentcore.KindSummary {
				summaries++
			}
		}
		rows := [][2]string{
			{"Used", contextLine(st.Context, st.Window)},
			{"Window", transcript.Tokens(st.Window)},
			{"Messages", strconv.Itoa(messages)},
			{"Summaries", strconv.Itoa(summaries)},
		}
		return show(&panel.Text{Title: "Context", Tabs: []panel.Tab{{Body: func(w int) []string {
			return append([]string{meter(st.Context, st.Window, min(w, 40)), ""}, info(rows, w)...)
		}}}})
	}}
}

// meter draws how full the context window is.
func meter(used, window, width int) string {
	if window <= 0 {
		return ""
	}
	frac := min(float64(used)/float64(window), 1)
	n := int(frac * float64(width))
	color := theme.OKText
	switch {
	case frac > 0.85:
		color = theme.ErrorText
	case frac > 0.6:
		color = theme.WarmText
	}
	return color.Render(strings.Repeat("█", n)) + theme.FaintText.Render(strings.Repeat("░", width-n)) + theme.MutedText.Render(fmt.Sprintf(" %.0f%%", frac*100))
}

func contextLine(used, window int) string {
	if window <= 0 {
		return transcript.Tokens(used)
	}
	return fmt.Sprintf("%s / %s (%.0f%%)", transcript.Tokens(used), transcript.Tokens(window), float64(used)*100/float64(window))
}

func status(a *app.App, version string) Command {
	return Command{Name: "status", Description: "Show the session, usage and runtime", Run: func(string) tea.Cmd {
		conv := a.Current()
		st := conv.Status()
		var sess app.SessionInfo
		if sessions, err := a.Sessions(); err == nil {
			for _, s := range sessions {
				if s.ID == conv.ID() {
					sess = s
				}
			}
		}
		branch := conv.GitBranch()
		messages := len(conv.History())
		load := func() []panel.Tab {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			servers := a.MCPStatus(ctx)

			overview := [][2]string{
				{"Version", version},
				{"Model", st.Provider + " · " + st.Model},
				{"Mode", string(st.Mode)},
				{"Context", contextLine(st.Context, st.Window)},
				{"Cost", cost(st.Usage)},
				{"Directory", transcript.ShortPath(st.Cwd)},
			}
			if st.Worktree != "" {
				overview = append(overview, [2]string{"Worktree", transcript.ShortPath(st.Worktree)})
			}
			if branch != "" {
				overview = append(overview, [2]string{"Branch", branch})
			}

			session := [][2]string{{"ID", st.SessionID}, {"Messages", strconv.Itoa(messages)}}
			if sess.Path != "" {
				session = append(session,
					[2]string{"Started", sess.Created.Format("2006-01-02 15:04") + " (" + age(time.Since(sess.Created)) + ")"},
					[2]string{"File", transcript.ShortPath(sess.Path)})
			}
			if st.Tasks > 0 {
				session = append(session, [2]string{"Background", fmt.Sprintf("%d running", st.Tasks)})
			}

			u := st.Usage
			usage := [][2]string{{"Input", transcript.Tokens(u.InputTokens)}, {"Output", transcript.Tokens(u.OutputTokens)}, {"Cost", cost(u)}}
			if u.CacheReadTokens+u.CacheWriteTokens > 0 {
				usage = append(usage, [2]string{"Cache", ""}, [2]string{"Read", transcript.Tokens(u.CacheReadTokens)}, [2]string{"Written", transcript.Tokens(u.CacheWriteTokens)})
				if u.InputTokens > 0 {
					usage = append(usage, [2]string{"Hit rate", fmt.Sprintf("%.0f%%", float64(u.CacheReadTokens)*100/float64(u.InputTokens))})
				}
			}
			if r := st.LastRun; r != nil {
				usage = append(usage, [2]string{"Last run", ""}, [2]string{"Turns", strconv.Itoa(r.Turns)}, [2]string{"Tool calls", strconv.Itoa(r.ToolCalls)}, [2]string{"Ended", string(r.Reason)})
			}

			effort := st.Effort
			if effort == "" {
				effort = "provider default"
			}
			runtime := [][2]string{{"Reasoning", effort}, {"Sub-agent model", st.SmallModel}, {"Extensions", ""}}
			runtime = append(runtime, [2]string{"MCP", mcpSummary(servers)})
			for _, s := range servers {
				runtime = append(runtime, [2]string{"  " + s.Name, mcpState(s)})
			}
			enabled := 0
			for _, p := range a.Plugins() {
				if p.State.Enabled {
					enabled++
				}
			}
			runtime = append(runtime,
				[2]string{"Plugins", fmt.Sprintf("%d enabled of %d", enabled, len(a.Plugins()))},
				[2]string{"Skills", strconv.Itoa(len(conv.Skills()))},
				[2]string{"Hooks", hooks(a)})

			tab := func(name string, rows [][2]string) panel.Tab {
				return panel.Tab{Name: name, Body: func(w int) []string { return info(rows, w) }}
			}
			return []panel.Tab{tab("overview", overview), tab("session", session), tab("usage", usage), tab("runtime", runtime)}
		}
		return show(&panel.Text{Title: "Status", Load: load})
	}}
}

func cost(u agentcore.Usage) string {
	if u.InputTokens+u.OutputTokens == 0 {
		return "no usage yet"
	}
	total := 0.0
	if u.Cost != nil {
		total = u.Cost.Total
	}
	return fmt.Sprintf("$%.4f", total)
}

func worktree(a *app.App) Command {
	return Command{Name: "worktree", Args: "<name> | exit | discard", Description: "Work in an isolated git worktree", Idle: true, Run: func(arg string) tea.Cmd {
		conv := a.Current()
		if arg == "exit" || arg == "discard" {
			res, err := conv.ExitWorktree(arg == "discard")
			if err != nil {
				return fail(err.Error())
			}
			switch {
			case res.Kept:
				return output(fmt.Sprintf("Left worktree %q; its changes are kept for review in %s (branch %s).\nMerge them with git, then remove it with `git worktree remove %s`.", res.Slug, res.Dir, res.Branch, res.Dir))
			case res.HadChanges:
				return note(fmt.Sprintf("Left worktree %q and dropped its changes.", res.Slug))
			case res.BranchKept:
				return output(fmt.Sprintf("Left worktree %q. Its branch %s has commits merged nowhere else, so it stays; delete it with `git branch -D %s` once merged.", res.Slug, res.Branch, res.Branch))
			}
			return note(fmt.Sprintf("Left worktree %q; it had no changes.", res.Slug))
		}
		dir, err := conv.EnterWorktree(arg)
		if err != nil {
			return fail(err.Error())
		}
		return output("Working in the worktree " + dir + ". Changes here stay out of the main checkout; /worktree exit to review them, /worktree discard to drop them.")
	}}
}

func btw(a *app.App) Command {
	return Command{Name: "btw", Args: "<question>", Description: "Ask a side question, kept out of the conversation", Run: func(q string) tea.Cmd {
		if q == "" {
			return fail("Usage: /btw <question>")
		}
		conv := a.Current()
		return show(&panel.Text{Title: "btw: " + q, Load: func() []panel.Tab {
			answer, err := conv.Query(context.Background(), q)
			if err != nil {
				return []panel.Tab{{Body: panel.Lines(theme.ErrorText.Render(app.ErrorText(err)))}}
			}
			return []panel.Tab{{Body: func(w int) []string { return markdown.Render(answer, w) }}}
		}})
	}}
}

func agents(a *app.App) Command {
	return Command{Name: "agents", Description: "Watch the background sub-agents", Run: func(string) tea.Cmd {
		known := a.Current().Agents().KnownAgents()
		if len(known) == 0 {
			return note("No background sub-agents have run.")
		}
		var items []panel.Item
		for _, k := range known {
			state := "done"
			if k.Active {
				state = "running"
			}
			items = append(items, panel.Item{Title: k.Name, Detail: state, Value: k.Name})
		}
		return show(&panel.List{Title: "Agents", Items: items, Select: func(it panel.Item) tea.Cmd {
			return emit(OpenAgent{Name: it.Value.(string)})
		}})
	}}
}

func tasks(a *app.App) Command {
	return Command{Name: "tasks", Description: "Manage background shells and agents", Run: func(string) tea.Cmd {
		rt := a.Current().Tasks()
		items := func() []panel.Item {
			var out []panel.Item
			for _, e := range rt.List() {
				group := "Shells"
				title := e.Command
				if e.Type == task.TypeSubAgent {
					group, title = "Agents", e.Agent
				}
				if e.Description != "" {
					title = e.Description
				}
				out = append(out, panel.Item{Group: group, Title: title, Detail: taskState(e), Value: e.ID})
			}
			return out
		}
		first := items()
		if len(first) == 0 {
			return note("No background tasks.")
		}
		return show(&panel.List{
			Title:  "Tasks",
			Items:  first,
			Reload: items,
			Hint:   theme.Hint("↑↓", "select", "enter", "details", "x", "stop", "esc", "close"),
			Select: func(it panel.Item) tea.Cmd {
				e, ok := rt.Get(it.Value.(string))
				if !ok {
					return nil
				}
				return show(&panel.Text{Title: it.Title, Tabs: []panel.Tab{{Body: func(w int) []string { return taskDetail(e, w) }}}})
			},
			Keys: func(k string, it *panel.Item) (tea.Cmd, bool, bool) {
				if k != "x" || it == nil {
					return nil, false, false
				}
				rt.Stop(it.Value.(string))
				return nil, true, false
			},
		})
	}}
}

func taskState(e task.Entry) string {
	d := transcript.Duration(time.Since(e.StartedAt))
	if !e.EndedAt.IsZero() {
		d = transcript.Duration(e.EndedAt.Sub(e.StartedAt))
	}
	switch e.Status {
	case task.Running:
		return "running · " + d
	case task.Failed:
		return "failed · " + d
	}
	if e.Type == task.TypeShell && e.ExitCode != 0 {
		return fmt.Sprintf("exit %d · %s", e.ExitCode, d)
	}
	return string(e.Status) + " · " + d
}

func taskDetail(e task.Entry, width int) []string {
	rows := [][2]string{{"Status", taskState(e)}}
	if e.Command != "" {
		rows = append(rows, [2]string{"Command", e.Command})
	}
	if e.Agent != "" {
		rows = append(rows, [2]string{"Agent", e.Agent}, [2]string{"Usage", fmt.Sprintf("%d tools · ↑%s ↓%s", e.ToolCount, transcript.Tokens(e.TokensIn), transcript.Tokens(e.TokensOut))})
	}
	if e.Error != "" {
		rows = append(rows, [2]string{"Error", e.Error})
	}
	out := info(rows, width)
	text := e.Result
	if e.Type == task.TypeShell && e.OutputFile != "" {
		text = fileTail(e.OutputFile, 16<<10)
	}
	if text = strings.TrimSpace(text); text != "" {
		out = append(out, "")
		out = append(out, markdown.Wrap(text, theme.MutedText, width)...)
	}
	return out
}

// fileTail returns the last n bytes of the file at path.
func fileTail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(st.Size()-n, 0)
	buf := make([]byte, st.Size()-off)
	_, _ = f.ReadAt(buf, off)
	return string(buf)
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
