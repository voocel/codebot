package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
)

// TeamCreateTool activates a Team in the current session. A Team is the home
// for long-lived peer agents (teammates) that communicate via the team
// mailbox. The leader (this agent) is auto-registered with the reserved name
// "team-lead". Subsequent teammates are spawned via the subagent tool with a
// team_name parameter (wired in Stage B).
//
// Only one team can be active per session — the leader's role is single-team
// to keep the coordination surface small.
type TeamCreateTool struct {
	reg      *team.Registry
	leaderID string // identifies the leader entry in the registry (typically session ID)
}

// teamNameRegexp restricts names to a conservative subset so the value is
// safe for log lines, file paths (future), and the agentId "name@team" format.
// '@' is forbidden because it splits agentId; spaces are forbidden because
// SendMessage uses bare names as targets.
var teamNameRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// NewTeamCreateTool constructs the tool. leaderID is recorded as the leader's
// task ID in the registry so future tools (e.g. drain inbox) can identify the
// leader entry. nil reg is a programmer error.
func NewTeamCreateTool(reg *team.Registry, leaderID string) *TeamCreateTool {
	return &TeamCreateTool{reg: reg, leaderID: leaderID}
}

func (t *TeamCreateTool) Name() string  { return "team_create" }
func (t *TeamCreateTool) Label() string { return "Create Team" }

func (t *TeamCreateTool) Description() string {
	return `Create a team in the current session. A team is a group of long-lived peer agents that can talk to each other through a shared mailbox.

Use this when the user asks to coordinate multiple agents, or when a task is large enough that parallel agents (e.g. researcher + tester + coder) would clearly speed it up. For one-shot exploration or a single background task, use the subagent tool instead.

After creating a team you are the team-lead. Spawn teammates via the subagent tool with a team_name parameter, then send messages to them with send_message. Only ONE team can be active per session.`
}

func (t *TeamCreateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("team_name", schema.String(
			"Short identifier for the team (letters, digits, '.', '_', '-'; up to 64 chars). Used as the routing prefix for agent IDs.",
		)).Required(),
		schema.Property("description", schema.String(
			"Optional one-line purpose of the team (shown in listings).",
		)),
	)
}

func (t *TeamCreateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		TeamName    string `json:"team_name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	name := strings.TrimSpace(a.TeamName)
	if name == "" {
		return json.Marshal("Validation error: team_name is required")
	}
	if !teamNameRegexp.MatchString(name) {
		return json.Marshal("Validation error: team_name must start with a letter or digit and contain only letters, digits, '.', '_', '-' (max 64 chars)")
	}

	if err := t.reg.CreateTeam(name, strings.TrimSpace(a.Description), t.leaderID); err != nil {
		if errors.Is(err, team.ErrTeamExists) {
			existing := t.reg.Team()
			return json.Marshal(fmt.Sprintf("A team is already active in this session (%q). Use team_delete first if you need to start a new one.", existing.Name))
		}
		return nil, err
	}

	return json.Marshal(map[string]any{
		"success":     true,
		"team_name":   name,
		"leader_name": team.TeamLeadName,
		"message":     fmt.Sprintf("Team %q created. You are %q. Spawn teammates via the subagent tool with team_name=%q.", name, team.TeamLeadName, name),
	})
}

// Static assertion: TeamCreateTool implements agentcore.Tool.
var _ agentcore.Tool = (*TeamCreateTool)(nil)

// NewTeamTools returns the team-coordination tools. team_create activates a
// team; send_message routes peer messages — to teammates by name or to
// subagents by task ID — so the LLM has one unified messaging surface.
// team_dismiss is the leader's graceful-shutdown surface.
// rt is required so send_message can deliver to subagents; pass it even when
// no team is active.
func NewTeamTools(reg *team.Registry, rt *task.Runtime, leaderID string) []agentcore.Tool {
	if reg == nil {
		return nil
	}
	tools := []agentcore.Tool{
		NewTeamCreateTool(reg, leaderID),
		NewTeamDismissTool(reg),
	}
	if rt != nil {
		tools = append(tools, NewSendMessageTool(rt, reg))
	}
	return tools
}
