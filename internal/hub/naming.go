package hub

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// NextSessionName derives a tmux session name for a new session in dir:
// the directory basename with '.' and ':' mapped to '-' (tmux forbids
// them), then the first of name, name-2, name-3, … not in existing.
func NextSessionName(existing []string, dir string) string {
	base := strings.Map(func(r rune) rune {
		if r == '.' || r == ':' {
			return '-'
		}
		return r
	}, filepath.Base(dir))
	name := base
	for n := 2; slices.Contains(existing, name); n++ {
		name = base + "-" + strconv.Itoa(n)
	}
	return name
}

// ForkCommand is claudeCmd resuming sessionID as a new conversation:
// --fork-session gives the copy its own id, so the two diverge without
// writing into each other's transcript. The id is read back from a pane
// option any process on the socket can set, so it is quoted.
func ForkCommand(claudeCmd, sessionID string) string {
	return claudeCmd + " --resume " + shellQuote(sessionID) + " --fork-session"
}
