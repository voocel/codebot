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
	cbteam "github.com/voocel/codebot/internal/team"
)

// TeamCreateTool renames the session's pre-created team to a meaningful
// label. A default team is always active from session start, so this tool's
// job is no longer "activate" — it's "give the team a name that reflects
// what we're working on". Once teammates have been spawned the rename is
// rejected (their agentIds would silently break).
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
	return `Rename the session's team to a meaningful label that reflects the current project (e.g. "auth-refactor", "bug-triage").

A default team is always active from session start — you don't need to call this before spawning teammates. Use it only when you want a clearer label than the default. The rename succeeds only before any teammate has been spawned (teammates' agent IDs embed the team name at spawn time, so renaming after the fact would silently break message routing).

Spawn teammates via the subagent tool (team_name is optional and defaults to the active team); coordinate with them via send_message.`
}

func (t *TeamCreateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("team_name", schema.String(
			"New short identifier for the team (letters, digits, '.', '_', '-'; up to 64 chars). Used as the routing suffix for agent IDs.",
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
	description := strings.TrimSpace(a.Description)

	// Default team is pre-created at session startup, so this is always a
	// rename in practice. CreateTeam is kept as a fallback for the unlikely
	// case the registry has no team (e.g. after a future team_dismiss).
	existing := t.reg.Team()
	if existing == nil {
		if err := t.reg.CreateTeam(name, description, t.leaderID); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"success":     true,
			"team_name":   name,
			"leader_name": team.TeamLeadName,
			"message":     fmt.Sprintf("Team %q created. You are %q. Spawn teammates via the subagent tool.", name, team.TeamLeadName),
		})
	}

	if existing.Name == name {
		return json.Marshal(map[string]any{
			"success":     true,
			"team_name":   name,
			"leader_name": team.TeamLeadName,
			"message":     fmt.Sprintf("Team is already named %q. Spawn teammates via the subagent tool.", name),
		})
	}

	if err := t.reg.RenameTeam(name, description); err != nil {
		if errors.Is(err, team.ErrTeamHasMembers) {
			return json.Marshal(fmt.Sprintf("Cannot rename team %q — teammates are already registered and their agent IDs embed the team name. Dismiss the team first if you really need to rename.", existing.Name))
		}
		return nil, err
	}

	// Wording: from the model's perspective, renaming the still-default team
	// is the very first naming event — surface it as "created" to match the
	// user's mental model. A rename away from a name the model itself chose
	// gets the literal wording so the model sees what actually changed.
	var message string
	if existing.Name == cbteam.DefaultTeamName {
		message = fmt.Sprintf("Team %q created. Spawn teammates via the subagent tool.", name)
	} else {
		message = fmt.Sprintf("Team renamed from %q to %q. Spawn teammates via the subagent tool.", existing.Name, name)
	}
	return json.Marshal(map[string]any{
		"success":     true,
		"team_name":   name,
		"leader_name": team.TeamLeadName,
		"message":     message,
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
