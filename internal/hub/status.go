package hub

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Status is derived fresh from each poll — never stored.
type Status int

const (
	StatusIdle Status = iota
	StatusWorking
	StatusDone // recently finished working, unseen — set by DoneTracker
	StatusNeedsInput
)

func (s Status) String() string {
	switch s {
	case StatusNeedsInput:
		return "NEEDS INPUT"
	case StatusDone:
		return "done"
	case StatusWorking:
		return "working"
	default:
		return "idle"
	}
}

// StatusFor derives a pane's status from its title and bell flag.
// Rules from live observation (2026-07-23): Claude Code titles a working
// session with a braille spinner frame ("⠂ Simplify tmux session…"), rings
// the bell / adds 🔔 when it needs input, and otherwise shows "✳ Claude
// Code" or "✻ <name>".
// Claude Code also publishes its own status per process (ClaudeSessions)
// and that beats every heuristic here — including a bell flag tmux
// latched during an earlier needs-input episode.
func StatusFor(p Pane) Status {
	if p.Claude != nil {
		if st, ok := p.Claude.status(); ok {
			return st
		}
	}
	if p.Bell || strings.Contains(p.Title, "🔔") {
		return StatusNeedsInput
	}
	if r, _ := utf8.DecodeRuneInString(p.Title); r >= 0x2800 && r <= 0x28FF {
		return StatusWorking
	}
	return StatusIdle
}

// dialogOption matches a Claude Code dialog's selected option row
// ("❯ 1. Yes"). The bare input caret ("❯ ") has no number, and plain
// numbered lists in conversation text have no caret, so this only fires
// on an actual open dialog.
var dialogOption = regexp.MustCompile(`(?m)^\s*❯\s*\d+\.\s`)

// screenTail is how many trailing non-empty lines of a capture count as
// "the bottom of the screen" — dialogs render there; anything higher is
// scrollback history.
const screenTail = 15

// ansiRe strips CSI sequences (colors, cursor) and OSC sequences
// (titles): capture-pane -e output is full of them, and the dialog
// markers must be matched on plain text.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)?`)

// NeedsInputScreen reports whether a pane's visible tail shows an open
// Claude Code dialog (permission prompt, question menu, trust prompt).
// Needed because attached sessions never latch tmux's bell flag and the
// pane title doesn't distinguish an open dialog from idle.
func NeedsInputScreen(screen string) bool {
	screen = ansiRe.ReplaceAllString(screen, "")
	screen = strings.ReplaceAll(screen, " ", " ") // Claude pads the caret with NBSP
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > screenTail {
		lines = lines[len(lines)-screenTail:]
	}
	tail := strings.Join(lines, "\n")
	return dialogOption.MatchString(tail) || strings.Contains(tail, "Enter to select")
}

// DeriveStatuses fills each pane's Status. Title and bell flag decide
// working/needs-input; panes that look idle get their screen captured
// (when capture is non-nil) to catch open dialogs the title can't show.
// Capture errors leave the pane idle — it likely just died and the next
// poll will drop it.
func DeriveStatuses(panes []Pane, capture func(pane string) (string, error)) {
	for i := range panes {
		st := StatusFor(panes[i])
		// The capture exists to catch dialogs the title can't show; a
		// pane publishing its own status has already told us.
		if st == StatusIdle && capture != nil && !panes[i].claudeStatusKnown() {
			if screen, err := capture(panes[i].ID); err == nil && NeedsInputScreen(screen) {
				st = StatusNeedsInput
			}
		}
		panes[i].Status = st
	}
}

// SortPanes orders panes for display: contiguous repo groups (Pane.Repo)
// in alphabetical order, rows within a group in tmux's own order
// (stable). Status never moves a row — a jumpy list is worse than a
// glance at the status column, and tab jumps to whatever needs input.
//
// The arbiter always sorts last, whatever its workdir is named. It is
// coop's own infrastructure, not work, so it gets a pinned row under
// the list rather than drifting through the repo groups alphabetically;
// the nav and paneAt both take "the arbiter is at the bottom" from here.
func SortPanes(panes []Pane) {
	sort.SliceStable(panes, func(i, j int) bool {
		if panes[i].Arbiter != panes[j].Arbiter {
			return !panes[i].Arbiter
		}
		return panes[i].Repo() < panes[j].Repo()
	})
}

// StripANSI removes CSI and OSC escape sequences — capture-pane -e
// output, for consumers that need plain text (coop peek, DialogLine).
func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// numberedRow matches any dialog option row, selected ("❯ 1. Yes") or
// not ("  2. No") — the lines DialogLine must walk past to find the
// question above them.
var numberedRow = regexp.MustCompile(`^\s*(❯\s*)?\d+\.\s`)

// DialogLine returns the dialog's question — the nearest non-empty,
// non-option line above the first selected option row — or "" when the
// screen shows no dialog. Feeds the audit log's dialog excerpt.
func DialogLine(screen string) string {
	screen = StripANSI(screen)
	screen = strings.ReplaceAll(screen, " ", " ") // Claude pads the caret with NBSP
	lines := strings.Split(screen, "\n")
	for i, l := range lines {
		if !dialogOption.MatchString(l) {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if s := strings.TrimSpace(lines[j]); s != "" && !numberedRow.MatchString(lines[j]) {
				return s
			}
		}
		return ""
	}
	return ""
}
