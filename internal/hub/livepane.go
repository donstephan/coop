package hub

import (
	"fmt"
	"strings"
)

// LiveMarker is the pane user option that tags the hub's live preview
// pane, so a restarted TUI adopts it instead of stacking new splits.
const LiveMarker = "@coop_live"

// AttachCmd is the shell command the live pane runs: a nested client of
// the target session on the same socket. TMUX must be cleared or tmux
// refuses to nest; "=" makes the target exact-match; ignore-size keeps
// this client out of window-size negotiation so the session stays sized
// by its primary (Ghostty) client.
func AttachCmd(socket, session string) string {
	return fmt.Sprintf("TMUX= exec tmux -L %s attach -t %s -f ignore-size",
		shq(socket), shq("="+session))
}

// PlaceholderCmd fills the live pane when nothing is selected. It blocks
// forever: remain-on-exit is only set after the split, so the command
// must not exit before then.
func PlaceholderCmd() string {
	return `printf '\n  no session selected\n'; exec sleep infinity`
}

// shq single-quotes s for POSIX sh.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseClientFlags reports whether any line of client_flags output names
// a client that takes part in sizing — i.e. anything but the hub's own
// ignore-size preview client.
func parseClientFlags(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.TrimSpace(line); f != "" && !strings.Contains(f, "ignore-size") {
			return true
		}
	}
	return false
}

// parseMarkedPane extracts the pane id whose user option value is "1".
func parseMarkedPane(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if f := splitFields(line); len(f) == 2 && f[1] == "1" {
			return f[0]
		}
	}
	return ""
}
