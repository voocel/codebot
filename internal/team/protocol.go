// Package team owns the wire protocol codebot uses on top of agentcore's
// team primitives. agentcore/team supplies the mailbox/registry/runner
// mechanism; this package supplies the format and policy choices:
//
//   - <teammate-message teammate_id=... color=... summary=...> XML envelopes
//     for everything the model sees;
//   - idle_notification JSON envelopes carrying the teammate's last
//     assistant text across agent boundaries;
//   - shutdown_request JSON envelopes for graceful teammate exit;
//   - the priority order (shutdown > leader > peer, FIFO within tier).
//
// Keeping these out of agentcore means a future project can plug a different
// envelope format (e.g. plain JSON, OpenAI tool-call shape) into the same
// runner without forking agentcore.
package team

import (
	"encoding/json"
	"fmt"
	"strings"

	coreteam "github.com/voocel/agentcore/team"
)

// xmlTeammateMessageTag is the wrapping element used in every prompt the
// teammate model sees. Kept as a single constant so format/parse can't drift.
const xmlTeammateMessageTag = "teammate-message"

// DefaultTeamName is the placeholder name bootstrap assigns to the team
// pre-created at session startup. Tools that surface team state to the model
// (team_create's result message in particular) use it to recognise the
// "still on the default name" case and word their response accordingly.
const DefaultTeamName = "default"

// Message kinds carried in the JSON "type" field. Plain peer messages have no
// envelope at all — their text flows through verbatim.
const (
	kindIdleNotification = "idle_notification"
	kindShutdownRequest  = "shutdown_request"
)

// FormatTeammateAttachment wraps text into the XML envelope the model expects.
// `from` is required; `color` and `summary` are optional and omitted when blank.
func FormatTeammateAttachment(from, text, color, summary string) string {
	var sb strings.Builder
	sb.Grow(len(text) + len(from) + 64)
	sb.WriteString("<")
	sb.WriteString(xmlTeammateMessageTag)
	sb.WriteString(` teammate_id="`)
	sb.WriteString(from)
	sb.WriteString(`"`)
	if color != "" {
		sb.WriteString(` color="`)
		sb.WriteString(color)
		sb.WriteString(`"`)
	}
	if summary != "" {
		sb.WriteString(` summary="`)
		sb.WriteString(summary)
		sb.WriteString(`"`)
	}
	sb.WriteString(">\n")
	sb.WriteString(text)
	sb.WriteString("\n</")
	sb.WriteString(xmlTeammateMessageTag)
	sb.WriteString(">")
	return sb.String()
}

// ParseTeammateAttachment is the inverse of FormatTeammateAttachment. Returns
// (from, body, true) on a valid envelope, ("", "", false) on anything that
// doesn't match — broken envelopes fall through to plain rendering at callers.
//
// Tolerant of attribute ordering, missing optional attributes, and surrounding
// whitespace; strict about needing a non-empty teammate_id and a matching
// closing tag.
func ParseTeammateAttachment(s string) (from, body string, ok bool) {
	s = strings.TrimSpace(s)
	const openPrefix = "<" + xmlTeammateMessageTag
	const closeTag = "</" + xmlTeammateMessageTag + ">"
	if !strings.HasPrefix(s, openPrefix) {
		return "", "", false
	}
	end := strings.Index(s, ">")
	if end < 0 {
		return "", "", false
	}
	attrs := s[len(openPrefix):end]
	if !strings.HasSuffix(s, closeTag) {
		return "", "", false
	}
	bodyStart := end + 1
	bodyEnd := len(s) - len(closeTag)
	if bodyEnd <= bodyStart {
		return "", "", false
	}
	body = strings.TrimSpace(s[bodyStart:bodyEnd])

	const key = `teammate_id="`
	i := strings.Index(attrs, key)
	if i < 0 {
		return "", "", false
	}
	j := strings.Index(attrs[i+len(key):], `"`)
	if j < 0 {
		return "", "", false
	}
	from = attrs[i+len(key) : i+len(key)+j]
	if from == "" {
		return "", "", false
	}
	return from, body, true
}

// EncodeIdleNotification produces the JSON envelope the leader-side pump
// inspects to surface a teammate's turn output. `text` is the teammate's last
// assistant message — empty for tool-only turns, in which case the pump
// injects a short status line instead.
func EncodeIdleNotification(from, text string) string {
	payload := map[string]string{
		"type": kindIdleNotification,
		"from": from,
	}
	if text != "" {
		payload["text"] = text
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// IdleNotificationText extracts the assistant text from an idle envelope.
// Returns "" when text is not an idle envelope or carries no text.
func IdleNotificationText(text string) string {
	if messageKind(text) != kindIdleNotification {
		return ""
	}
	var probe struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return ""
	}
	return probe.Text
}

// EncodeShutdownRequest produces the JSON envelope that triggers a teammate's
// graceful exit. The teammate runner picks this up at the next turn boundary
// (top priority, see PickPriority) and returns from Run without consulting
// the model. `reason` is for transcript/UI only.
func EncodeShutdownRequest(reason string) string {
	b, _ := json.Marshal(map[string]string{
		"type":   kindShutdownRequest,
		"reason": reason,
	})
	return string(b)
}

// IsIdleNotification reports whether text is the idle-wake envelope. Exported
// so the leader-side pump can filter these before injecting into the prompt
// stream (when no text is present it surfaces a status line instead of the
// envelope itself).
func IsIdleNotification(text string) bool { return messageKind(text) == kindIdleNotification }

// IsShutdownRequest reports whether text is a shutdown_request envelope.
func IsShutdownRequest(text string) bool { return messageKind(text) == kindShutdownRequest }

// messageKind returns the "type" field if text is a JSON envelope, else "".
// Shared by every probe so the JSON parse runs at most once per call site
// even when several kinds are tested in sequence.
func messageKind(text string) string {
	trimmed := strings.TrimLeft(text, " \t\n\r")
	if !strings.HasPrefix(trimmed, "{") {
		return ""
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return ""
	}
	return probe.Type
}

// FallbackIdleStatus is the human-readable line the leader sees when a teammate
// ends a turn with no assistant text (tool-only turn). The pump uses it as a
// fall-through after IdleNotificationText returns "".
func FallbackIdleStatus(from string) string {
	return fmt.Sprintf("%s is idle and ready for the next instruction.", from)
}

// PickPriority chooses the highest-priority message index from queue.
// Tier order: shutdown_request (0) > team-lead (1) > peer (2). Within a tier,
// lowest index wins, preserving FIFO arrival.
//
// Scanning shutdown across the whole queue first prevents a peer-DM flood from
// starving shutdown handling.
func PickPriority(queue []coreteam.Message) int {
	bestIdx, bestTier := 0, 3
	for i, m := range queue {
		tier := 2
		if IsShutdownRequest(m.Text) {
			tier = 0
		} else if m.From == coreteam.TeamLeadName {
			tier = 1
		}
		if tier < bestTier {
			bestTier, bestIdx = tier, i
			if tier == 0 {
				break
			}
		}
	}
	return bestIdx
}

// Hooks returns the agentcore protocol-hook bundle wired with codebot's
// XML/JSON formats and priority policy. Spawn callers pass this through to
// the runner so a teammate behaves identically to the original hard-coded
// version of the runner.
//
// The synthetic initial prompt and every later inbound message both flow
// through FormatPrompt — agentcore packages the initial prompt as Message{
// From: TeamLeadName, Text: InitialPrompt, Summary: Description}, so the
// single XML wrapper handles both cases.
func Hooks() coreteam.ProtocolHooks {
	return coreteam.ProtocolHooks{
		FormatPrompt: func(m coreteam.Message) string {
			return FormatTeammateAttachment(m.From, m.Text, m.Color, m.Summary)
		},
		EncodeIdle:      EncodeIdleNotification,
		ShouldTerminate: IsShutdownRequest,
		PickPriority:    PickPriority,
	}
}
