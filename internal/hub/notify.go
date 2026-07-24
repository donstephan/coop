package hub

import "os/exec"

// NotifyTracker announces panes entering needs-input, at most once per
// episode across every hub instance on the socket. Like DoneTracker,
// the cross-poll memory lives on the tracked panes themselves as a user
// option (NotifiedMarker): whichever hub polls first sets the marker
// and reports the pane; the marker keeps every other hub (and later
// polls) quiet until the pane leaves needs-input, which re-arms it.
// Writes are best-effort — a failed write means the next poll retries,
// at worst duplicating a notification.
type NotifyTracker struct {
	tmux Tmux
}

// NewNotifyTracker returns a tracker persisting state through tm.
func NewNotifyTracker(tm Tmux) *NotifyTracker {
	return &NotifyTracker{tmux: tm}
}

// Apply runs after DeriveStatuses and returns the panes that just
// entered needs-input and should be announced. Safe on a nil tracker
// (no-op).
func (t *NotifyTracker) Apply(panes []Pane) []Pane {
	if t == nil {
		return nil
	}
	var fresh []Pane
	for i := range panes {
		p := &panes[i]
		switch {
		case p.Status == StatusNeedsInput && !p.NotifiedMark:
			t.tmux.SetPaneOption(p.ID, NotifiedMarker, "1")
			fresh = append(fresh, *p)
		case p.Status != StatusNeedsInput && p.NotifiedMark:
			t.tmux.UnsetPaneOption(p.ID, NotifiedMarker)
		}
	}
	return fresh
}

// notifyArgs builds the notify-send argv: the session as the summary
// line, the task (what Claude was doing, per its pane title) as the
// body — omitted when empty so title-less panes get a one-liner.
func notifyArgs(session, task string) []string {
	args := []string{"-a", "coop", session + " needs input"}
	if task != "" {
		args = append(args, task)
	}
	return args
}

// NotifySend pops a desktop notification for a session awaiting input
// via notify-send (libnotify). Best-effort: on a machine without
// notify-send (or without a notification daemon) it silently does
// nothing.
func NotifySend(session, task string) {
	exec.Command("notify-send", notifyArgs(session, task)...).Run()
}
