package main

import (
	"io"
	"path/filepath"
	"strings"
	"time"

	"coop/internal/hub"
)

// isHookCmd reports whether argv selects the hook subcommand — the
// status publisher Claude Code invokes (via the injected settings file)
// on every hook event.
func isHookCmd(args []string) bool {
	return len(args) > 0 && args[0] == "hook"
}

// hookSocket resolves the socket for this hook invocation from $TMUX
// alone — deliberately not cliSocket, and deliberately ignoring
// $COOP_SOCKET. cliSocket lets COOP_SOCKET win because the arbiter's
// verbs name the hub's socket explicitly and may run outside any tmux
// pane at all; the hook has no such freedom. It runs inside the exact
// pane it's about to mark, so the only socket that can be right is the
// one that pane actually lives on. Honoring an exported COOP_SOCKET here
// would have the hook write status onto a different tmux server — pane
// ids are per-server, so the write would silently mark nothing, or
// worse, some unrelated pane that happens to share the id.
func hookSocket(tmuxEnv string) string {
	if path, _, ok := strings.Cut(tmuxEnv, ","); ok && path != "" {
		return filepath.Base(path)
	}
	return "coop"
}

// runHookCLI publishes one hook event onto this pane's user options.
// It always returns 0 and prints nothing: hook stdout is parsed by
// Claude Code as JSON and stderr lands in the session's transcript, so
// every failure here is a silent no-op — the pane just stays on the
// title fallback until the next event.
func runHookCLI(stdin io.Reader, getenv func(string) string) int {
	pane := getenv("TMUX_PANE")
	tmuxEnv := getenv("TMUX")
	if pane == "" || tmuxEnv == "" {
		return 0 // not in a tmux pane coop could mark
	}
	p, ok := hub.ParseHookPayload(stdin)
	if !ok {
		return 0
	}
	tm := &hub.ExecTmux{Socket: hookSocket(tmuxEnv)}
	// Best-effort: a failed write is one stale status; the next event
	// or the SessionEnd unset corrects it.
	_ = hub.ApplyHook(tm, pane, p, time.Now())
	// The nudge shares the event's process: fires once per dialog, needs
	// no hub attached, and carries the payload's substance.
	hub.HookNudge(tm, pane, p, time.Now())
	return 0
}
