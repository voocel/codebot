package commands

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

func compact(a *app.App) Command {
	return Command{Name: "compact", Description: "Summarize the conversation to free context", Idle: true, Run: func(string) tea.Cmd {
		conv := a.Current()
		return func() tea.Msg {
			// The transcript shows the compaction from its events.
			if err := conv.Compact(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
				return transcript.Fail("Compaction failed: " + app.ErrorText(err))
			}
			return nil
		}
	}}
}

func newSession(a *app.App) Command {
	return Command{Name: "new", Aliases: []string{"clear"}, Description: "Start a new conversation", Idle: true, Run: func(string) tea.Cmd {
		return Open(a, "")
	}}
}

// Open runs off the TUI goroutine because opening publishes events to it.
// An empty id opens a new conversation.
func Open(a *app.App, id string) tea.Cmd {
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
				Detail:  fmt.Sprintf("%s · %d messages", transcript.Ago(time.Since(s.Updated)), s.MessageCount),
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
			return Open(a, it.Value.(string))
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

func rewind(a *app.App) Command {
	return Command{Name: "rewind", Description: "Go back to before an earlier request: its files, the conversation, or both", Idle: true, Run: func(string) tea.Cmd {
		conv := a.Current()
		cps := conv.Checkpoints()
		if len(cps) == 0 {
			return note("Nothing to go back to yet.")
		}
		var items []panel.Item
		for _, cp := range slices.Backward(cps) {
			items = append(items, panel.Item{Title: app.Printable(firstLine(cp.Prompt)), Detail: transcript.Ago(time.Since(cp.Time)), Value: cp})
		}
		return show(&panel.List{Title: "Rewind", Items: items, Filter: true, Select: func(it panel.Item) tea.Cmd {
			cp := it.Value.(app.Checkpoint)
			// Off the TUI goroutine: comparing the files runs git.
			return func() tea.Msg { return rewindChoices(conv, cp) }
		}})
	}}
}

// Rewound reports a rewind; the conversation's going back reloads it, and
// the request goes back to the editor.
type Rewound struct {
	Prompt       string
	Conversation bool
	Note         string
}

// rewindChoices offers to put back the files as well as the conversation
// only when files changed since and can go back.
func rewindChoices(conv *app.Conversation, cp app.Checkpoint) tea.Msg {
	type choice struct{ files, conversation bool }
	before := "before “" + app.Printable(firstLine(cp.Prompt)) + "”"
	changed, err := conv.Changed(cp)
	n := fmt.Sprintf("%d %s", len(changed), plural(len(changed), "file"))
	items := []panel.Item{
		{Title: "Files and conversation", Detail: n, Value: choice{true, true}},
		{Title: "Conversation only", Detail: "the files stay as they are", Value: choice{false, true}},
		{Title: "Files only", Detail: n + "; the agent is told", Value: choice{true, false}},
	}
	switch {
	case err != nil:
		items = []panel.Item{{Title: "Conversation only", Detail: "the files can't go back: " + err.Error(), Value: choice{false, true}}}
	case len(changed) == 0:
		items = []panel.Item{{Title: "Conversation only", Detail: "no file changed since", Value: choice{false, true}}}
	}
	return &panel.List{Title: "Go back to " + before, Items: items, Select: func(it panel.Item) tea.Cmd {
		c := it.Value.(choice)
		return func() tea.Msg {
			changed, err := conv.Rewind(cp, c.files, c.conversation)
			if err != nil {
				return transcript.Fail("Could not go back: " + err.Error())
			}
			files := fmt.Sprintf("%d %s", len(changed), plural(len(changed), "file"))
			r := Rewound{Prompt: cp.Prompt, Conversation: c.conversation}
			switch {
			case !c.conversation && len(changed) == 0:
				r.Note = "The files were already as they were " + before + "."
			case !c.conversation:
				r.Note = "Put back " + files + " as they were " + before + "; the agent is told."
			case c.files:
				r.Note = "Went back to " + before + ", with " + files + "."
			default:
				r.Note = "Went back to " + before + "."
			}
			return r
		}
	}}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

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

// status is the one place to see what is in effect: the conversation, what
// it has used, the extensions and the settings.
func status(a *app.App, version string) Command {
	return Command{Name: "status", Description: "Show the conversation, usage, extensions and settings", Run: func(string) tea.Cmd {
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
		history := conv.History()
		summaries := 0
		for _, m := range history {
			if m.Kind == agentcore.KindSummary {
				summaries++
			}
		}
		load := func() []panel.Tab {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			servers := a.MCPStatus(ctx)

			effort := st.Effort
			if effort == "" {
				effort = "provider default"
			}
			used := contextLine(st.Context, st.Window)
			if summaries > 0 {
				used += fmt.Sprintf(" · compacted %d %s", summaries, plural(summaries, "time"))
			}
			overview := [][2]string{
				{"Model", st.Provider + " · " + st.Model},
				{"Reasoning", effort},
				{"Mode", string(st.Mode)},
				{"Context", used},
				{"Cost", cost(st.Usage)},
				{"Directory", transcript.HomePath(st.Cwd)},
			}
			if st.Worktree != "" {
				overview = append(overview, [2]string{"Worktree", transcript.HomePath(st.Worktree)})
			}
			if branch != "" {
				overview = append(overview, [2]string{"Branch", branch})
			}
			overview = append(overview, [2]string{"Session", ""}, [2]string{"ID", st.SessionID}, [2]string{"Messages", strconv.Itoa(len(history))})
			if sess.Path != "" {
				overview = append(overview,
					[2]string{"Started", sess.Created.Format("2006-01-02 15:04") + " (" + transcript.Ago(time.Since(sess.Created)) + ")"},
					[2]string{"File", transcript.ShortPath(sess.Path)})
			}
			if st.Tasks > 0 {
				overview = append(overview, [2]string{"Background", fmt.Sprintf("%d running · /tasks", st.Tasks)})
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

			tab := func(name string, rows [][2]string) panel.Tab {
				return panel.Tab{Name: name, Body: func(w int) []string { return info(rows, w) }}
			}
			return []panel.Tab{
				{Name: "overview", Body: func(w int) []string {
					return append([]string{meter(st.Context, st.Window, min(w, 40)), ""}, info(overview, w)...)
				}},
				tab("usage", usage),
				tab("extensions", extensionRows(a, servers)),
				tab("settings", settingRows(a, st, version)),
			}
		}
		return show(&panel.Text{Title: "Status", Load: load})
	}}
}

func settingRows(a *app.App, st app.Status, version string) [][2]string {
	s := a.Settings()
	pc := s.Providers[st.Provider]
	base := pc.BaseURL
	if base == "" {
		base = "default"
	}
	rows := [][2]string{
		{"Version", version},
		{"User file", transcript.ShortPath(config.UserSettingsPath())},
	}
	if root := a.Trust().Root; root != "" {
		rows = append(rows, [2]string{"Project file", transcript.ShortPath(config.ProjectSettingsPath(root))})
	}
	rows = append(rows,
		[2]string{st.Provider, ""}, [2]string{"API key", maskKey(pc.APIKey)}, [2]string{"Base URL", base},
		[2]string{"Runtime", ""}, [2]string{"Max turns", fmt.Sprint(s.MaxTurns)}, [2]string{"Sub-agent model", st.SmallModel})
	if s.CompactRatio > 0 {
		rows = append(rows, [2]string{"Compact at", fmt.Sprintf("%.0f%%", s.CompactRatio*100)})
	}
	rows = append(rows, [2]string{"Providers", ""})
	for _, name := range slices.Sorted(maps.Keys(s.Providers)) {
		p := s.Providers[name]
		desc := fmt.Sprintf("%d %s", len(p.Models), plural(len(p.Models), "model"))
		if p.BaseURL != "" {
			desc += " · " + p.BaseURL
		}
		if name == st.Provider {
			name += " ✓"
		}
		rows = append(rows, [2]string{name, desc})
	}
	return rows
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "••••"
	}
	return key[:4] + "…" + key[len(key)-4:]
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

func tasks(a *app.App) Command {
	return Command{Name: "tasks", Description: "Watch and stop the background shells and agents", Run: func(string) tea.Cmd {
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
			Hint:   theme.Hint("↑↓", "select", "enter", "open", "x", "stop", "esc", "close"),
			// An agent opens on its run, live; a shell on its output.
			Select: func(it panel.Item) tea.Cmd {
				e, ok := rt.Get(it.Value.(string))
				switch {
				case !ok:
					return nil
				case e.Type == task.TypeSubAgent:
					return emit(OpenAgent{Run: e.Run, Title: it.Title})
				}
				return show(&panel.Text{Title: it.Title, Tabs: []panel.Tab{{Body: func(w int) []string { return shellDetail(e, w) }}}})
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

// shellDetail shows a background shell; an agent opens on its run instead.
func shellDetail(e task.Entry, width int) []string {
	rows := [][2]string{{"Status", taskState(e)}, {"Command", e.Command}}
	if e.Error != "" {
		rows = append(rows, [2]string{"Error", e.Error})
	}
	out := info(rows, width)
	if text := strings.TrimSpace(fileTail(e.OutputFile, 16<<10)); text != "" {
		out = append(out, "")
		out = append(out, markdown.Wrap(text, theme.MutedText, width)...)
	}
	return out
}

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

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
