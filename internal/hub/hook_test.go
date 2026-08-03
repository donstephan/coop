package hub

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNudgeDetail(t *testing.T) {
	cases := []struct {
		name string
		p    HookPayload
		want string
	}{
		{"bash command", HookPayload{Event: "PermissionRequest", ToolName: "Bash",
			ToolInput: toolInput("npm test"), PermissionRule: "Bash(npm *)"}, "Bash: npm test"},
		{"rule fallback", HookPayload{Event: "PermissionRequest", ToolName: "WebFetch",
			PermissionRule: "WebFetch(domain:example.com)"}, "WebFetch: WebFetch(domain:example.com)"},
		{"bare tool", HookPayload{Event: "PermissionRequest", ToolName: "Edit"}, "Edit"},
		{"no payload", HookPayload{Event: "PermissionRequest"}, ""},
		// A malformed or stripped payload can carry a command with no
		// tool name — the command alone, not a bare leading "": ".
		{"command with no tool name", HookPayload{Event: "PermissionRequest",
			ToolInput: toolInput("npm test")}, "npm test"},
		{"question", HookPayload{Event: "Notification", NotificationType: "elicitation_dialog"}, "question"},
		{"agent question", HookPayload{Event: "Notification", NotificationType: "agent_needs_input"}, "agent question"},
		{"permission notify", HookPayload{Event: "Notification", NotificationType: "permission_prompt"}, "permission"},
		{"non-waiting event", HookPayload{Event: "Stop"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NudgeDetail(tc.p); got != tc.want {
				t.Errorf("NudgeDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseHookPayloadToolFields(t *testing.T) {
	p, ok := ParseHookPayload(strings.NewReader(
		`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"npm test"},"permission_rule":"Bash(npm *)"}`))
	if !ok || p.ToolName != "Bash" || p.ToolInput.Command != "npm test" || p.PermissionRule != "Bash(npm *)" {
		t.Fatalf("payload = %+v ok=%v", p, ok)
	}
}

// toolInput builds a HookPayload.ToolInput value in tests — the field
// is an anonymous struct, so callers can't just write a composite
// literal against the type by name.
func toolInput(cmd string) struct {
	Command string `json:"command"`
} {
	return struct {
		Command string `json:"command"`
	}{Command: cmd}
}

func TestParseHookPayload(t *testing.T) {
	p, ok := ParseHookPayload(strings.NewReader(
		`{"hook_event_name":"Notification","session_id":"s1","cwd":"/home/user/alpha","notification_type":"idle_prompt"}`))
	if !ok || p.Event != "Notification" || p.SessionID != "s1" ||
		p.CWD != "/home/user/alpha" || p.NotificationType != "idle_prompt" {
		t.Fatalf("payload = %+v ok=%v", p, ok)
	}
	if _, ok := ParseHookPayload(strings.NewReader("not json")); ok {
		t.Error("garbage parsed as payload")
	}
	if _, ok := ParseHookPayload(strings.NewReader(`{"session_id":"s1"}`)); ok {
		t.Error("payload without hook_event_name accepted")
	}
}

func TestApplyHookStatusMapping(t *testing.T) {
	now := time.Unix(1753900200, 0)
	cases := []struct {
		name    string
		payload HookPayload
		status  string // "" = no status write expected
	}{
		{"prompt", HookPayload{Event: "UserPromptSubmit"}, "busy"},
		{"post tool", HookPayload{Event: "PostToolUse"}, "busy"},
		{"denied", HookPayload{Event: "PermissionDenied"}, "busy"},
		{"permission", HookPayload{Event: "PermissionRequest"}, "waiting"},
		{"notify permission", HookPayload{Event: "Notification", NotificationType: "permission_prompt"}, "waiting"},
		{"notify question", HookPayload{Event: "Notification", NotificationType: "elicitation_dialog"}, "waiting"},
		{"notify agent", HookPayload{Event: "Notification", NotificationType: "agent_needs_input"}, "waiting"},
		{"notify idle", HookPayload{Event: "Notification", NotificationType: "idle_prompt"}, "idle"},
		{"notify other", HookPayload{Event: "Notification", NotificationType: "auth_success"}, ""},
		{"stop", HookPayload{Event: "Stop"}, "idle"},
		{"start fresh", HookPayload{Event: "SessionStart", Source: "startup"}, "idle"},
		{"start resume", HookPayload{Event: "SessionStart", Source: "resume"}, "idle"},
		{"start clear", HookPayload{Event: "SessionStart", Source: "clear"}, "idle"},
		{"start compact", HookPayload{Event: "SessionStart", Source: "compact"}, ""},
		{"start fork", HookPayload{Event: "SessionStart", Source: "fork"}, ""},
		{"unknown event", HookPayload{Event: "SubagentStop"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeTmux{}
			if err := ApplyHook(f, "%1", tc.payload, now); err != nil {
				t.Fatal(err)
			}
			if got := f.paneOpts["%1/"+ClaudeStatusMarker]; got != tc.status {
				t.Errorf("status = %q, want %q", got, tc.status)
			}
			wantSince := ""
			if tc.status != "" {
				wantSince = "1753900200"
			}
			if got := f.paneOpts["%1/"+ClaudeSinceMarker]; got != wantSince {
				t.Errorf("since = %q, want %q", got, wantSince)
			}
		})
	}
}

func TestApplyHookSessionStartIdentity(t *testing.T) {
	f := &fakeTmux{}
	// compact: identity still refreshes, status untouched
	err := ApplyHook(f, "%1", HookPayload{Event: "SessionStart", Source: "compact",
		SessionID: "s2", CWD: "/home/user/alpha"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if f.paneOpts["%1/"+ClaudeSessionMarker] != "s2" ||
		f.paneOpts["%1/"+ClaudeCWDMarker] != "/home/user/alpha" {
		t.Errorf("identity not written: %v", f.paneOpts)
	}
	if _, ok := f.paneOpts["%1/"+ClaudeStatusMarker]; ok {
		t.Error("compact wrote a status")
	}
}

func TestApplyHookSinceOnlyOnChange(t *testing.T) {
	f := &fakeTmux{}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(ApplyHook(f, "%1", HookPayload{Event: "UserPromptSubmit"}, time.Unix(100, 0)))
	must(ApplyHook(f, "%1", HookPayload{Event: "PostToolUse"}, time.Unix(200, 0)))
	if got := f.paneOpts["%1/"+ClaudeSinceMarker]; got != "100" {
		t.Errorf("busy→busy reset since to %q, want 100", got)
	}
	must(ApplyHook(f, "%1", HookPayload{Event: "Stop"}, time.Unix(300, 0)))
	if got := f.paneOpts["%1/"+ClaudeSinceMarker]; got != "300" {
		t.Errorf("busy→idle since = %q, want 300", got)
	}
}

// Identity isn't only a SessionStart write: every payload carries
// session_id/cwd, so a status change on a pane that never got a
// SessionStart (or whose one write failed) still heals it.
func TestApplyHookIdentityHealsOnStatusChange(t *testing.T) {
	f := &fakeTmux{}
	err := ApplyHook(f, "%1", HookPayload{Event: "UserPromptSubmit",
		SessionID: "s3", CWD: "/home/user/alpha"}, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if f.paneOpts["%1/"+ClaudeSessionMarker] != "s3" ||
		f.paneOpts["%1/"+ClaudeCWDMarker] != "/home/user/alpha" {
		t.Errorf("identity not healed on status change: %v", f.paneOpts)
	}
	// busy→busy: PaneOption reads the status unchanged, so the whole
	// branch — since AND identity — is skipped, not just since.
	f.paneOpts["%1/"+ClaudeSessionMarker] = "stale"
	if err := ApplyHook(f, "%1", HookPayload{Event: "PostToolUse",
		SessionID: "s3", CWD: "/home/user/alpha"}, time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	if f.paneOpts["%1/"+ClaudeSessionMarker] != "stale" {
		t.Error("busy→busy repeat rewrote identity")
	}
}

func TestApplyHookSessionEndClears(t *testing.T) {
	f := &fakeTmux{}
	if err := ApplyHook(f, "%1", HookPayload{Event: "SessionStart", Source: "startup",
		SessionID: "s1", CWD: "/home/user/alpha"}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyHook(f, "%1", HookPayload{Event: "SessionEnd"}, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if len(f.paneOpts) != 0 {
		t.Errorf("options left after SessionEnd: %v", f.paneOpts)
	}
}

// nudgePanes is a socket snapshot for HookNudge tests: our own claude
// pane %1 (session alpha), the arbiter, and a hub session.
func nudgePanes(arbAge time.Duration, now time.Time, selfNudged bool) []Pane {
	return []Pane{
		{Session: "alpha", ID: "%1", ArbiterNudgedMark: selfNudged},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "recommend",
			Created: now.Add(-arbAge)},
		{Session: "roost", ID: "%0", Hub: true},
	}
}

func TestHookNudge(t *testing.T) {
	now := time.Unix(1753900300, 0)
	waiting := HookPayload{Event: "PermissionRequest", SessionID: "s1",
		ToolName: "Bash"}
	waiting.ToolInput.Command = "npm test"

	t.Run("nudges the arbiter once", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 1 {
			t.Fatalf("sent = %v, want one nudge", f.sent)
		}
		want := `coop: session "alpha" needs input (mode: recommend; trigger: Bash: npm test)`
		if f.sent[0][0] != want || f.sent[0][1] != "Enter" {
			t.Errorf("keys = %v, want [%q Enter]", f.sent[0], want)
		}
		if f.paneOpts["%1/"+ArbiterNudgedMarker] != "1" {
			t.Error("nudged marker not set")
		}
	})
	t.Run("non-waiting event is a no-op", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%1", HookPayload{Event: "Stop"}, now)
		if len(f.sent) != 0 || len(f.paneOpts) != 0 {
			t.Errorf("no-op expected: sent=%v opts=%v", f.sent, f.paneOpts)
		}
	})
	t.Run("no arbiter: no nudge and NO marker", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)[:1]}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("sent = %v", f.sent)
		}
		// a later arbiter's launch catch-up must still find this pane
		// un-nudged
		if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
			t.Error("marker set with no arbiter running")
		}
	})
	t.Run("young arbiter is skipped, marker unset", func(t *testing.T) {
		// 1.5s clears the old arbiterReadyAge (1s) but not ArbiterReady's
		// +1s allowance for session_created's whole-second truncation —
		// this is exactly the gap the truncation-overshoot bug lived in.
		f := &fakeTmux{panes: nudgePanes(1500*time.Millisecond, now, false)}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("typed at a %v-old arbiter", 1500*time.Millisecond)
		}
		if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
			t.Error("marker set on skipped nudge")
		}
	})
	t.Run("already nudged: deduped", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, true)}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("second event re-nudged: %v", f.sent)
		}
	})
	t.Run("arbiter's own dialogs don't nudge", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%9", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("arbiter nudged itself: %v", f.sent)
		}
	})
	t.Run("send failure re-arms the marker", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false),
			sendErr: errors.New("gone")}
		HookNudge(f, "%1", waiting, now)
		if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
			t.Error("marker left set after failed send — catch-up could never retry")
		}
	})
	t.Run("self pane not in the list: no panic, no send", func(t *testing.T) {
		// coop hook always lists its own pane, but the list is a fresh
		// ListSessions call — a pane that raced its own death between the
		// event firing and the hook process running must not panic self's
		// nil dereference.
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%missing", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("nudged for a pane absent from the list: %v", f.sent)
		}
	})
	t.Run("Notification elicitation_dialog nudges with a detail", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%1", HookPayload{Event: "Notification",
			SessionID: "s1", NotificationType: "elicitation_dialog"}, now)
		if len(f.sent) != 1 {
			t.Fatalf("sent = %v, want one nudge", f.sent)
		}
		want := `coop: session "alpha" needs input (mode: recommend; trigger: question)`
		if f.sent[0][0] != want {
			t.Errorf("keys = %v, want [%q Enter]", f.sent[0], want)
		}
	})
}

// TestArbiterReady covers the boundary session_created's whole-second
// truncation makes real: the measured age can overshoot the actual age
// by just under a second, so the safe floor is arbiterReadyAge plus a
// full extra second, not arbiterReadyAge itself.
func TestArbiterReady(t *testing.T) {
	now := time.Unix(1753900300, 0)
	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"well under", 500 * time.Millisecond, false},
		{"at the old (wrong) floor", arbiterReadyAge, false},
		{"just under the true floor", arbiterReadyAge + time.Second - time.Millisecond, false},
		{"at the true floor", arbiterReadyAge + time.Second, true},
		{"well over", 5 * time.Second, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			arb := Pane{Created: now.Add(-c.age)}
			if got := ArbiterReady(arb, now); got != c.want {
				t.Errorf("ArbiterReady(age=%v) = %v, want %v", c.age, got, c.want)
			}
		})
	}
}

func TestApplyHookClearsEpisodeMarkers(t *testing.T) {
	f := &fakeTmux{}
	seed := func() {
		f.SetPaneOption("%1", ArbiterNudgedMarker, "1")
		f.SetPaneOption("%1", ArbiterNoteMarker, "check the diff")
		f.SetPaneOption("%1", ArbiterSuggestMarker, "2")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// waiting -> busy retires the episode
	must(ApplyHook(f, "%1", HookPayload{Event: "PermissionRequest"}, time.Unix(1, 0)))
	seed()
	must(ApplyHook(f, "%1", HookPayload{Event: "PostToolUse"}, time.Unix(2, 0)))
	for _, m := range []string{ArbiterNudgedMarker, ArbiterNoteMarker, ArbiterSuggestMarker} {
		if _, ok := f.paneOpts["%1/"+m]; ok {
			t.Errorf("%s survived waiting→busy", m)
		}
	}
	// busy -> busy (no transition) leaves a fresh episode's markers alone
	seed()
	must(ApplyHook(f, "%1", HookPayload{Event: "PostToolUse"}, time.Unix(3, 0)))
	if _, ok := f.paneOpts["%1/"+ArbiterNoteMarker]; !ok {
		t.Error("busy→busy cleared markers without a transition")
	}
}

// TestHookEventsCoverage binds hookEvents (hooksettings.go — what coop
// registers in the injected settings file) to hookAction (what ApplyHook
// actually does with each event). Nothing else enforces that pairing: an
// event added to one list and not the other would either register a
// hook Claude Code fires and coop silently ignores, or leave hookAction
// with a case that never runs because nothing invokes it.
func TestHookEventsCoverage(t *testing.T) {
	// One representative payload per event, shaped to take the branch
	// that actually produces a write (SessionStart needs a "startup"
	// source, Notification a needs-input type — see hookAction).
	samples := map[string]HookPayload{
		"SessionStart":      {Event: "SessionStart", Source: "startup", SessionID: "s1", CWD: "/home/user/alpha"},
		"UserPromptSubmit":  {Event: "UserPromptSubmit"},
		"PostToolUse":       {Event: "PostToolUse"},
		"PermissionRequest": {Event: "PermissionRequest"},
		"PermissionDenied":  {Event: "PermissionDenied"},
		"Notification":      {Event: "Notification", NotificationType: "permission_prompt"},
		"Stop":              {Event: "Stop"},
		"SessionEnd":        {Event: "SessionEnd"},
	}

	got := map[string]bool{}
	for _, ev := range hookEvents {
		got[ev] = true
	}
	if len(got) != len(hookEvents) {
		t.Fatalf("hookEvents has duplicate entries: %v", hookEvents)
	}
	if len(got) != len(samples) {
		t.Fatalf("hookEvents has %d events, sample map has %d — keep them in step",
			len(got), len(samples))
	}
	for ev := range samples {
		if !got[ev] {
			t.Errorf("sample event %q is not in hookEvents", ev)
		}
	}

	// Every registered event must produce a non-no-op action for its
	// sample shape — a status write, an identity refresh, or a clear.
	for ev, p := range samples {
		status, identity, clear := hookAction(p)
		if status == "" && !identity && !clear {
			t.Errorf("%s: hookAction is a no-op for %+v", ev, p)
		}
	}

	// And the reverse: hookAction must not react to an event name
	// hookEvents never registers — that would be a case nothing can
	// ever reach, the same drift from the other direction.
	for _, ev := range []string{"SubagentStop", "PreCompact", "PreToolUse"} {
		if got[ev] {
			t.Fatalf("test fixture %q collides with a real hookEvents entry", ev)
		}
		status, identity, clear := hookAction(HookPayload{Event: ev,
			Source: "startup", NotificationType: "permission_prompt"})
		if status != "" || identity || clear {
			t.Errorf("hookAction handles unregistered event %q", ev)
		}
	}
}
