package main

import (
	"strings"
	"testing"
)

// Outside a tmux pane (or with garbage stdin) the hook is a silent
// no-op that still exits 0 — a hook must never disturb its session.
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil map still answers every lookup with "", same as a
			// genuinely unset env var — no need for a separate branch.
			getenv := func(k string) string { return tc.vars[k] }
			if code := runHookCLI(strings.NewReader(tc.stdin), getenv); code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
		})
	}
}

func TestIsHookCmd(t *testing.T) {
	if !isHookCmd([]string{"hook"}) || isHookCmd([]string{"peek"}) || isHookCmd(nil) {
		t.Error("isHookCmd misdetects")
	}
}
