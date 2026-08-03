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

// hasBell reports the needs-input title signals: tmux's latched bell
// flag or the 🔔 Claude Code puts in the title.
func hasBell(p Pane) bool {
	return p.Bell || strings.Contains(p.Title, "🔔")
}

// titleSpinner reports a braille spinner frame leading the title —
// Claude Code's "working" marker.
func titleSpinner(title string) bool {
	r, _ := utf8.DecodeRuneInString(title)
	return r >= 0x2800 && r <= 0x28FF
}

// StatusFor derives a pane's status. The hook-published state (Claude,
// from the pane's @coop_claude_status options — see hook.go) beats the
// title heuristics, except where the title proves it stale: no hook
// event fires between approving a tool run and its PostToolUse, so
// waiting + spinner means the dialog was answered; and nothing fires on
// an esc-interrupt, so busy + bell means Claude rang for input since.
// Panes publishing nothing (launched outside coop) read from the title
// alone — rules from live observation (2026-07-23): braille spinner =
// working, bell/🔔 = needs input, otherwise idle.
func StatusFor(p Pane) Status {
	if p.Claude != nil {
		if st, ok := p.Claude.status(); ok {
			if st == StatusNeedsInput && titleSpinner(p.Title) {
				return StatusWorking
			}
			if st == StatusWorking && hasBell(p) {
				return StatusNeedsInput
			}
			return st
		}
	}
	if hasBell(p) {
		return StatusNeedsInput
	}
	if titleSpinner(p.Title) {
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
// Serves the arbiter's answer gate (Answer) — attached sessions never
// latch tmux's bell flag and the pane title doesn't distinguish an open
// dialog from idle, so the arbiter checks the actual screen before it
// types a reply.
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

// DeriveStatuses fills each pane's Status — derived fresh every poll,
// never stored.
func DeriveStatuses(panes []Pane) {
	for i := range panes {
		panes[i].Status = StatusFor(panes[i])
	}
}

// SortPanes orders panes for display: contiguous repo groups (Pane.Repo)
// in alphabetical order, rows within a group in tmux's own order
// (stable). Status never moves a row — a jumpy list is worse than a
// glance at the status column, and tab jumps to whatever needs input.
func SortPanes(panes []Pane) {
	sort.SliceStable(panes, func(i, j int) bool {
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
