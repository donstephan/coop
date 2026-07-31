package hub

import "time"

// ClaudeState is the state a session's injected coop hooks published
// onto its pane — @coop_claude_status and friends, written by
// ApplyHook, read back by parsePanes. Nil on panes publishing nothing
// (sessions launched outside coop, or from before hook injection),
// which fall back to the title heuristics in StatusFor.
type ClaudeState struct {
	SessionID   string    // names the transcript, ~/.claude/projects/<slug>/<id>.jsonl
	Status      string    // "busy" | "waiting" | "idle"
	StatusSince time.Time // when it entered Status (zero if unpublished)
	CWD         string
}

// status maps the published status onto ours. The bool is false for
// anything unrecognized — a value a later coop wrote — so callers fall
// back to the title rather than mislabel a busy pane idle.
func (s *ClaudeState) status() (Status, bool) {
	switch s.Status {
	case "busy":
		return StatusWorking, true
	case "waiting":
		return StatusNeedsInput, true
	case "idle":
		return StatusIdle, true
	}
	return StatusIdle, false
}

// Since is when the pane entered its current status: the hook-published
// since where there is one, else the session's start time. "waiting 6m"
// is what makes a row worth switching to; "session started 3h ago" says
// nothing about whether it needs you.
func (p Pane) Since() time.Time {
	if p.Claude != nil && !p.Claude.StatusSince.IsZero() {
		return p.Claude.StatusSince
	}
	return p.Created
}
