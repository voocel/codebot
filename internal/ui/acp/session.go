package acp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
)

// session is one session of the client: a conversation, and what it asks
// the user and reads files through.
type session struct {
	srv  *Server
	id   acp.SessionId
	conv *app.Conversation
	fs   *EditorFS

	// turn holds a prompt until its turn is over: the SDK cancels a prompt
	// that a newer one for the session supersedes, and the newer one must not
	// steer into the run being stopped.
	turn sync.Mutex

	mu sync.Mutex
	// stop stops the prompt in flight. session/cancel reaches the prompt
	// through the SDK, which cancels its context; session/close needs this.
	stop context.CancelFunc

	// streamed is set once the response under way sent text, which a retry
	// sends again. Events come one at a time.
	streamed bool
}

var _ interact.UI = (*session)(nil)

func (sess *session) send(u acp.SessionUpdate) {
	_ = sess.srv.conn.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: sess.id, Update: u})
}

// prompt answers once every update of the turn has been sent.
func (sess *session) prompt(ctx context.Context, blocks []litellm.Block) (acp.PromptResponse, error) {
	sess.turn.Lock()
	defer sess.turn.Unlock()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	sess.mu.Lock()
	sess.stop = stop
	sess.mu.Unlock()
	if ctx.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}

	conv := sess.conv
	first := len(conv.History()) == 0
	last := conv.Status().LastRun
	answer, err := conv.Prompt(ctx, blocks)
	if err == nil && ctx.Err() == nil {
		if first {
			sess.send(acp.SessionUpdate{SessionInfoUpdate: &acp.SessionSessionInfoUpdate{Title: acp.Ptr(title(blocks))}})
		}
		if answer != "" {
			sess.send(acp.UpdateAgentMessageText(answer))
		}
		err = conv.Wait(ctx)
	}
	if ctx.Err() != nil {
		// The cancel may have come before the input reached the run, so the
		// run is stopped here too. Its last updates go before the response.
		conv.Cancel()
		_ = conv.Wait(context.Background())
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	if err != nil {
		return acp.PromptResponse{}, err
	}
	run := conv.Status().LastRun
	if run == last {
		run = nil // a skill that forks answered without a run
	}
	return turnResult(run)
}

// close stops the prompt in flight, such as a skill that forks, and closes
// the conversation, which stops its run.
func (sess *session) close() {
	sess.mu.Lock()
	stop := sess.stop
	sess.mu.Unlock()
	if stop != nil {
		stop()
	}
	sess.conv.Close()
}

// title is the first line of the prompt's text, as the session list has it.
func title(blocks []litellm.Block) string {
	var text strings.Builder
	for _, b := range blocks {
		if t, ok := b.(litellm.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	line, _, _ := strings.Cut(strings.TrimSpace(text.String()), "\n")
	if r := []rune(line); len(r) > 80 {
		line = string(r[:80])
	}
	return line
}

// sendCommands lists the skills the user can invoke; Prompt runs them.
func (sess *session) sendCommands() {
	cmds := []acp.AvailableCommand{}
	for _, sk := range sess.conv.SkillCommands() {
		c := acp.AvailableCommand{Name: sk.Name, Description: sk.Description}
		if sk.ArgumentHint != "" {
			c.Input = &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: sk.ArgumentHint}}
		}
		cmds = append(cmds, c)
	}
	sess.send(acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{AvailableCommands: cmds}})
}

// Approve offers a persistent allow only when there is a rule to remember.
func (sess *session) Approve(ctx context.Context, p interact.Approval) (interact.Verdict, error) {
	opts := []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow_once"}}
	if p.Remember != "" {
		opts = append(opts, acp.PermissionOption{Kind: acp.PermissionOptionKindAllowAlways, Name: "Always allow " + p.Remember, OptionId: "allow_always"})
	}
	opts = append(opts, acp.PermissionOption{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"})

	title := p.Summary
	if title == "" {
		title = p.Tool
	}
	if p.Unsandboxed {
		title += " (outside the sandbox)"
	}
	// A hook command has no tool call of its own for the client to show, so
	// the approval is labeled with the tool name.
	id := p.ToolID
	if id == "" {
		id = p.Tool
	}
	resp, err := sess.srv.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: sess.id,
		Options:   opts,
		ToolCall:  acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(id), Title: acp.Ptr(title), Kind: acp.Ptr(toolKind(p.Tool))},
	})
	if err != nil {
		return interact.Verdict{Choice: interact.Deny}, err
	}
	choice := interact.Deny // also when cancelled, with nothing selected
	if resp.Outcome.Selected != nil {
		switch resp.Outcome.Selected.OptionId {
		case "allow_once":
			choice = interact.AllowOnce
		case "allow_always":
			choice = interact.AllowAlways
		}
	}
	return interact.Verdict{Choice: choice}, nil
}

// ACP can't pose questions, so the conversation runs without ask_user.
func (sess *session) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}

var modeDescriptions = map[interact.Mode]string{
	interact.ModeStrict:      "Ask before edits; block commands",
	interact.ModeBalanced:    "Ask before edits and commands",
	interact.ModeAcceptEdits: "Edit freely; ask before commands",
	interact.ModeTrust:       "Run everything without asking",
}

// modes are for clients that predate config options.
func (sess *session) modes() *acp.SessionModeState {
	var modes []acp.SessionMode
	for _, m := range interact.Modes {
		modes = append(modes, acp.SessionMode{Id: acp.SessionModeId(m), Name: string(m), Description: acp.Ptr(modeDescriptions[m])})
	}
	return &acp.SessionModeState{AvailableModes: modes, CurrentModeId: acp.SessionModeId(sess.conv.Mode())}
}

// The config options: the mode, the model and, when the model takes one, its
// reasoning effort, "default" being the provider's.
const (
	optionMode   = "mode"
	optionModel  = "model"
	optionEffort = "thought_level"

	defaultEffort = "default"
)

func (sess *session) configOptions() []acp.SessionConfigOption {
	a := sess.srv.app
	prov, name, effort := sess.conv.Model()

	var modes acp.SessionConfigSelectOptionsUngrouped
	for _, m := range interact.Modes {
		modes = append(modes, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(m), Name: string(m), Description: acp.Ptr(modeDescriptions[m])})
	}
	mode := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(sess.conv.Mode()), acp.SessionConfigSelectOptions{Ungrouped: &modes})
	mode.Select.Id, mode.Select.Name, mode.Select.Category = optionMode, "Mode", acp.Ptr(acp.SessionConfigOptionCategoryMode)

	model := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(prov+"/"+name), acp.SessionConfigSelectOptions{Grouped: acp.Ptr(models(a.Settings().Providers, prov, name))})
	model.Select.Id, model.Select.Name, model.Select.Category = optionModel, "Model", acp.Ptr(acp.SessionConfigOptionCategoryModel)
	out := []acp.SessionConfigOption{mode, model}

	if levels := a.ThinkingLevels(prov, name); len(levels) > 1 {
		var efforts acp.SessionConfigSelectOptionsUngrouped
		for _, l := range levels {
			efforts = append(efforts, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(effortValue(l)), Name: effortValue(l)})
		}
		effort := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(effortValue(effort)), acp.SessionConfigSelectOptions{Ungrouped: &efforts})
		effort.Select.Id, effort.Select.Name, effort.Select.Category = optionEffort, "Reasoning", acp.Ptr(acp.SessionConfigOptionCategoryThoughtLevel)
		out = append(out, effort)
	}
	return out
}

// models are the providers' models by provider, with the current one,
// prov/name, among them even when its provider doesn't list it.
func models(providers map[string]config.ProviderConfig, prov, name string) acp.SessionConfigSelectOptionsGrouped {
	listed := make(map[string][]string, len(providers)+1)
	for p, pc := range providers {
		listed[p] = pc.Models
	}
	if !slices.Contains(listed[prov], name) {
		listed[prov] = append(slices.Clone(listed[prov]), name)
	}
	out := acp.SessionConfigSelectOptionsGrouped{}
	for _, p := range slices.Sorted(maps.Keys(listed)) {
		if len(listed[p]) == 0 {
			continue
		}
		g := acp.SessionConfigSelectGroup{Group: acp.SessionConfigGroupId(p), Name: p}
		for _, n := range listed[p] {
			g.Options = append(g.Options, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(p + "/" + n), Name: n})
		}
		out = append(out, g)
	}
	return out
}

func effortValue(effort string) string {
	if effort == "" {
		return defaultEffort
	}
	return effort
}

func (sess *session) setOption(id acp.SessionConfigId, value acp.SessionConfigValueId) error {
	prov, name, effort := sess.conv.Model()
	switch id {
	case optionMode:
		mode, err := interact.ParseMode(string(value))
		if err != nil {
			return fmt.Errorf("acp: %w", err)
		}
		sess.conv.SetMode(mode)
		return nil
	case optionModel:
		prov, name, ok := strings.Cut(string(value), "/")
		if !ok {
			return fmt.Errorf("acp: no model %q", value)
		}
		if !slices.Contains(sess.srv.app.ThinkingLevels(prov, name), effort) {
			effort = ""
		}
		return sess.conv.SetModel(prov, name, effort)
	case optionEffort:
		effort := string(value)
		if effort == defaultEffort {
			effort = ""
		}
		return sess.conv.SetModel(prov, name, effort)
	}
	return fmt.Errorf("acp: no option %q", id)
}

// modeChanged keeps the mode the same in both places a client may show it.
func (sess *session) modeChanged(m interact.Mode) {
	sess.send(acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{CurrentModeId: acp.SessionModeId(m)}})
	sess.send(acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{ConfigOptions: sess.configOptions()}})
}
