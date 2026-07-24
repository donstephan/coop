package hub

import (
	"strconv"
	"time"
)

// DoneTracker upgrades idle panes that recently finished working to
// StatusDone — the one status that needs memory across polls. That
// memory lives on the tracked panes themselves as user options
// (WorkingMarker, DoneSinceMarker), so every hub instance on the socket
// shares one view: any hub arms a badge, and a visit through any hub
// clears it everywhere. Options die with their pane, so there is no
// stale state to prune. Writes are best-effort — a failed write leaves
// the options unchanged and the next poll retries the transition.
type DoneTracker struct {
	ttl  time.Duration
	tmux Tmux
}

// NewDoneTracker returns a tracker with the given decay TTL, persisting
// state through tm. ttl <= 0 disables the Done state entirely.
func NewDoneTracker(ttl time.Duration, tm Tmux) *DoneTracker {
	return &DoneTracker{ttl: ttl, tmux: tm}
}

// Apply runs after DeriveStatuses and before SortPanes. A pane whose
// status fell from working to idle turns Done until the user visits its
// session (visited reports that), the TTL elapses, or it leaves idle
// again. needs-input always wins over Done. visited is only called for
// panes currently armed Done, so callers may back it with tmux queries.
// Safe on a nil tracker (no-op).
func (t *DoneTracker) Apply(panes []Pane, visited func(session string) bool, now time.Time) {
	if t == nil || t.ttl <= 0 {
		return
	}
	for i := range panes {
		p := &panes[i]
		switch p.Status {
		case StatusWorking:
			if !p.WorkingMark {
				t.tmux.SetPaneOption(p.ID, WorkingMarker, "1")
			}
			if !p.DoneSince.IsZero() {
				t.tmux.UnsetPaneOption(p.ID, DoneSinceMarker)
			}
		case StatusNeedsInput:
			if p.WorkingMark {
				t.tmux.UnsetPaneOption(p.ID, WorkingMarker)
			}
			if !p.DoneSince.IsZero() {
				t.tmux.UnsetPaneOption(p.ID, DoneSinceMarker)
			}
		default: // idle
			since := p.DoneSince
			if p.WorkingMark {
				// Fell out of working this poll — arm the badge.
				since = now
				t.tmux.SetPaneOption(p.ID, DoneSinceMarker,
					strconv.FormatInt(now.Unix(), 10))
				t.tmux.UnsetPaneOption(p.ID, WorkingMarker)
			}
			if since.IsZero() {
				continue
			}
			if now.Sub(since) >= t.ttl || (visited != nil && visited(p.Session)) {
				t.tmux.UnsetPaneOption(p.ID, DoneSinceMarker)
				continue
			}
			p.Status = StatusDone
		}
	}
}
