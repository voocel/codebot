package team

import (
	"encoding/json"
	"strings"
	"testing"

	coreteam "github.com/voocel/agentcore/team"
)

// ---------------------------------------------------------------------------
// FormatTeammateAttachment / ParseTeammateAttachment must round-trip and
// reject malformed envelopes.
// ---------------------------------------------------------------------------

func TestFormatTeammateAttachment_StructureAndOptionals(t *testing.T) {
	cases := []struct {
		name             string
		from, text       string
		color, summary   string
		mustContain      []string
		mustNotContain   []string
		expectExactCount map[string]int // substring → count
	}{
		{
			name: "minimal — no color, no summary",
			from: "alice", text: "hello",
			mustContain:    []string{`teammate_id="alice"`, "hello"},
			mustNotContain: []string{`color="`, `summary="`},
		},
		{
			name: "with color only",
			from: "alice", text: "hello", color: "blue",
			mustContain:    []string{`color="blue"`},
			mustNotContain: []string{`summary="`},
		},
		{
			name: "with summary only",
			from: "alice", text: "hello", summary: "draft",
			mustContain:    []string{`summary="draft"`},
			mustNotContain: []string{`color="`},
		},
		{
			name: "with both",
			from: "alice", text: "hello", color: "blue", summary: "draft",
			mustContain: []string{`teammate_id="alice"`, `color="blue"`, `summary="draft"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatTeammateAttachment(tc.from, tc.text, tc.color, tc.summary)
			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q: %s", want, got)
				}
			}
			for _, banned := range tc.mustNotContain {
				if strings.Contains(got, banned) {
					t.Errorf("output should not contain %q: %s", banned, got)
				}
			}
			if !strings.HasPrefix(got, "<teammate-message ") || !strings.HasSuffix(got, "</teammate-message>") {
				t.Errorf("envelope wrapper wrong: %q", got)
			}
		})
	}
}

func TestParseTeammateAttachment_RoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantFrom string
		wantBody string
		wantOK   bool
	}{
		{"minimal", FormatTeammateAttachment("alice", "hello world", "", ""), "alice", "hello world", true},
		{"with color and summary", FormatTeammateAttachment("alice", "found bug", "blue", "result"), "alice", "found bug", true},
		{"multiline body", FormatTeammateAttachment("bob", "line1\nline2\nline3", "", ""), "bob", "line1\nline2\nline3", true},
		{"team-lead reserved name", FormatTeammateAttachment(coreteam.TeamLeadName, "ack", "", ""), coreteam.TeamLeadName, "ack", true},
		{"not an envelope", "just plain text", "", "", false},
		{"missing closer", `<teammate-message teammate_id="alice">incomplete`, "", "", false},
		{"empty teammate_id", `<teammate-message teammate_id="">x</teammate-message>`, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, body, ok := ParseTeammateAttachment(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (input=%q)", ok, tc.wantOK, tc.input)
			}
			if !ok {
				return
			}
			if from != tc.wantFrom {
				t.Errorf("from = %q, want %q", from, tc.wantFrom)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// idle_notification + shutdown_request envelope probes.
// ---------------------------------------------------------------------------

func TestEncodeIdleNotification_OmitsEmptyText(t *testing.T) {
	with := EncodeIdleNotification("alice", "I found the bug at line 42.")
	without := EncodeIdleNotification("alice", "")

	if !IsIdleNotification(with) || !IsIdleNotification(without) {
		t.Fatalf("envelopes must be recognised as idle_notification: %q / %q", with, without)
	}
	if IdleNotificationText(with) != "I found the bug at line 42." {
		t.Errorf("IdleNotificationText round-trip failed: %q", with)
	}
	if IdleNotificationText(without) != "" {
		t.Errorf("empty text envelope should produce empty IdleNotificationText, got %q", IdleNotificationText(without))
	}

	// `text` field should be entirely absent (not just empty) when no text is
	// provided — keeps the wire format clean for downstream consumers.
	var probe map[string]any
	if err := json.Unmarshal([]byte(without), &probe); err != nil {
		t.Fatalf("envelope not valid JSON: %v", err)
	}
	if _, hasText := probe["text"]; hasText {
		t.Errorf("envelope without text should omit the text field; got %v", probe)
	}
}

func TestIsIdleNotification_RejectsOthers(t *testing.T) {
	if IsIdleNotification("hello") {
		t.Error("plain text should not match idle_notification")
	}
	if IsIdleNotification(EncodeShutdownRequest("done")) {
		t.Error("shutdown_request must not match idle_notification")
	}
	if !IsIdleNotification(EncodeIdleNotification("a", "")) {
		t.Error("freshly-encoded idle_notification must match")
	}
}

func TestEncodeShutdownRequest_RecognisableAndCarriesReason(t *testing.T) {
	env := EncodeShutdownRequest("task complete")
	if !IsShutdownRequest(env) {
		t.Fatalf("encoded envelope not recognised: %q", env)
	}
	var probe struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(env), &probe); err != nil {
		t.Fatalf("envelope not valid JSON: %v", err)
	}
	if probe.Type != "shutdown_request" || probe.Reason != "task complete" {
		t.Errorf("envelope payload = %+v, want type=shutdown_request reason='task complete'", probe)
	}
}

func TestIsShutdownRequest_RejectsOthers(t *testing.T) {
	if IsShutdownRequest("plain text") {
		t.Error("plain text should not match shutdown_request")
	}
	if IsShutdownRequest(EncodeIdleNotification("a", "b")) {
		t.Error("idle_notification must not match shutdown_request")
	}
}

func TestFallbackIdleStatus_MentionsTheTeammate(t *testing.T) {
	out := FallbackIdleStatus("researcher")
	if !strings.Contains(out, "researcher") {
		t.Errorf("fallback should mention teammate name: %q", out)
	}
}

// ---------------------------------------------------------------------------
// PickPriority — codebot's leader-first / shutdown-overrides-all policy.
// ---------------------------------------------------------------------------

func TestPickPriority(t *testing.T) {
	cases := []struct {
		name    string
		queue   []coreteam.Message
		wantIdx int
	}{
		{
			"single peer",
			[]coreteam.Message{{From: "alice", Text: "hi"}},
			0,
		},
		{
			"leader beats peer",
			[]coreteam.Message{{From: "alice", Text: "hi"}, {From: coreteam.TeamLeadName, Text: "do X"}},
			1,
		},
		{
			"shutdown beats everything",
			[]coreteam.Message{
				{From: coreteam.TeamLeadName, Text: "do X"},
				{From: "alice", Text: "hi"},
				{From: coreteam.TeamLeadName, Text: EncodeShutdownRequest("done")},
			},
			2,
		},
		{
			"FIFO within tier",
			[]coreteam.Message{
				{From: coreteam.TeamLeadName, Text: "first"},
				{From: coreteam.TeamLeadName, Text: "second"},
			},
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PickPriority(tc.queue); got != tc.wantIdx {
				t.Errorf("PickPriority = %d, want %d", got, tc.wantIdx)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Hooks() wires every field — agentcore's Run depends on all four being set
// to behave like the original hard-coded version.
// ---------------------------------------------------------------------------

func TestHooks_AllFieldsWired(t *testing.T) {
	h := Hooks()
	if h.FormatPrompt == nil ||
		h.EncodeIdle == nil ||
		h.ShouldTerminate == nil ||
		h.PickPriority == nil {
		t.Fatalf("Hooks() left a nil field: %+v", h)
	}

	// The initial prompt path: agentcore packages InitialPrompt + Description
	// as Message{From: TeamLeadName, Summary: Description}, so FormatPrompt
	// must produce the leader's XML envelope when fed that shape.
	init := h.FormatPrompt(coreteam.Message{From: coreteam.TeamLeadName, Text: "hello", Summary: "kickoff"})
	if !strings.Contains(init, `teammate_id="team-lead"`) || !strings.Contains(init, "hello") || !strings.Contains(init, `summary="kickoff"`) {
		t.Errorf("FormatPrompt on the synthetic initial Message should produce team-lead envelope with summary, got %q", init)
	}

	msg := h.FormatPrompt(coreteam.Message{From: "alice", Text: "hi", Color: "blue"})
	if !strings.Contains(msg, `teammate_id="alice"`) || !strings.Contains(msg, `color="blue"`) {
		t.Errorf("FormatPrompt should produce envelope with from + color, got %q", msg)
	}

	if !IsIdleNotification(h.EncodeIdle("alice", "")) {
		t.Errorf("EncodeIdle must produce a recognisable idle envelope")
	}
	if !h.ShouldTerminate(EncodeShutdownRequest("")) {
		t.Errorf("ShouldTerminate must accept shutdown_request envelopes")
	}
	if h.ShouldTerminate("plain text") {
		t.Errorf("ShouldTerminate must reject plain text")
	}
}
