package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Outside a tmux pane (or with garbage stdin) the hook is a silent
// no-op that still exits 0 — a hook must never disturb its session.
// Silent is asserted on stdout too: Claude Code parses it as JSON, and a
// stray byte from an event with nothing to say would land in the
// session's context.
func TestRunHookCLINoOps(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
		vars  map[string]string
	}{
		{"no env", `{"hook_event_name":"Stop"}`, nil},
		{"bad stdin", "not json", map[string]string{
			"TMUX_PANE": "%1", "TMUX": "/tmp/tmux-1000/coop,1,0"}},
		// $TMUX set but no pane id: e.g. a hook fired from a script run
		// by hand in a tmux window rather than from Claude Code itself.
		{"tmux set, no pane", `{"hook_event_name":"Stop"}`, map[string]string{
			"TMUX": "/tmp/tmux-1000/coop,1,0"}},
		// A SessionStart on an unshimmed session: the note is the one
		// thing this command prints, and a repo with no toolbox says
		// nothing at all.
		{"session start, no toolbox", `{"hook_event_name":"SessionStart"}`,
			map[string]string{"PATH": "/usr/bin:/bin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil map still answers every lookup with "", same as a
			// genuinely unset env var — no need for a separate branch.
			getenv := func(k string) string { return tc.vars[k] }
			var out bytes.Buffer
			if code := runHookCLI(strings.NewReader(tc.stdin), &out, getenv); code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
		})
	}
}

// The session note rides out on SessionStart as additionalContext, and
// only on SessionStart: every other event's stdout is parsed for a
// decision this command never makes.
func TestRunHookCLIEmitsSessionNote(t *testing.T) {
	old := sessionNote
	sessionNote = func(func(string) string) string { return "shimmed: go python3" }
	t.Cleanup(func() { sessionNote = old })

	// Every SessionStart source, compact emphatically included: a note
	// read once at launch is gone the first time the window is squeezed,
	// which is exactly when the session has stopped knowing this.
	for _, source := range []string{"startup", "resume", "clear", "compact", "fork"} {
		var out bytes.Buffer
		in := `{"hook_event_name":"SessionStart","source":"` + source + `"}`
		runHookCLI(strings.NewReader(in), &out, func(string) string { return "" })
		var got hookOutput
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%s: stdout is not the documented hook JSON: %v (%q)",
				source, err, out.String())
		}
		if got.HookSpecificOutput.HookEventName != "SessionStart" {
			t.Errorf("%s: hookEventName = %q", source, got.HookSpecificOutput.HookEventName)
		}
		if got.HookSpecificOutput.AdditionalContext != "shimmed: go python3" {
			t.Errorf("%s: additionalContext = %q", source, got.HookSpecificOutput.AdditionalContext)
		}
	}

	// And nothing else prints, however much there is to say: Claude Code
	// parses these events' stdout for decisions coop never makes.
	for _, event := range []string{"Stop", "PostToolUse", "PermissionRequest", "SessionEnd"} {
		var out bytes.Buffer
		in := `{"hook_event_name":"` + event + `"}`
		runHookCLI(strings.NewReader(in), &out, func(string) string { return "" })
		if out.Len() != 0 {
			t.Errorf("%s printed %q, want empty", event, out.String())
		}
	}
}

func TestIsHookCmd(t *testing.T) {
	if !isHookCmd([]string{"hook"}) || isHookCmd([]string{"peek"}) || isHookCmd(nil) {
		t.Error("isHookCmd misdetects")
	}
}
