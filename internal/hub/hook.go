package hub

import (
	"encoding/json"
	"io"
	"strconv"
	"time"
)

// HookPayload is the subset of Claude Code's hook stdin JSON that coop
// reads (documented contract: session_id, cwd, hook_event_name in every
// event; source on SessionStart; notification_type on Notification).
type HookPayload struct {
	Event            string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	CWD              string `json:"cwd"`
	Source           string `json:"source"`
	NotificationType string `json:"notification_type"`

	// PermissionRequest extras — what the dialog is about, so a nudge
	// can carry the substance and spare the arbiter a peek round-trip.
	ToolName       string `json:"tool_name"`
	PermissionRule string `json:"permission_rule"`
	ToolInput      struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// ParseHookPayload decodes one hook invocation's stdin. false for
// anything unusable — the caller no-ops rather than guessing.
func ParseHookPayload(r io.Reader) (HookPayload, bool) {
	var p HookPayload
	if json.NewDecoder(r).Decode(&p) != nil || p.Event == "" {
		return HookPayload{}, false
	}
	return p, true
}

// hookAction maps an event onto the pane writes it implies. status ""
// means no status change; identity means refresh session_id/cwd; clear
// means the session ended and every option goes.
func hookAction(p HookPayload) (status string, identity, clear bool) {
	switch p.Event {
	case "SessionStart":
		identity = true
		// compact and fork fire mid-turn — refreshing identity is
		// right, flipping a busy row idle is not.
		switch p.Source {
		case "startup", "resume", "clear":
			status = "idle"
		}
	case "UserPromptSubmit", "PostToolUse", "PermissionDenied":
		status = "busy"
	case "PermissionRequest":
		status = "waiting"
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt", "elicitation_dialog", "agent_needs_input":
			status = "waiting"
		case "idle_prompt":
			status = "idle"
		}
	case "Stop":
		status = "idle"
	case "SessionEnd":
		clear = true
	}
	return
}

// claudeMarkers is every option ApplyHook manages, for SessionEnd.
var claudeMarkers = []string{
	ClaudeStatusMarker, ClaudeSinceMarker, ClaudeSessionMarker, ClaudeCWDMarker,
}

// ApplyHook writes one hook event's state onto the pane. The since
// option only moves when the status actually changes, so a busy→busy
// PostToolUse doesn't reset "working 5m" to zero.
func ApplyHook(tm Tmux, pane string, p HookPayload, now time.Time) error {
	status, identity, clear := hookAction(p)
	if clear {
		// Best-effort: keep going through every marker even if one unset
		// fails, and report the first error rather than bailing after it
		// — a marker left behind here self-heals anyway once the pane
		// dies (tmux discards its options with it), so partial cleanup
		// beats leaving the rest of the four stuck for no reason.
		var first error
		for _, m := range claudeMarkers {
			if err := tm.UnsetPaneOption(pane, m); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	if identity {
		if err := tm.SetPaneOption(pane, ClaudeSessionMarker, p.SessionID); err != nil {
			return err
		}
		if err := tm.SetPaneOption(pane, ClaudeCWDMarker, p.CWD); err != nil {
			return err
		}
	}
	if status == "" {
		return nil
	}
	if cur, err := tm.PaneOption(pane, ClaudeStatusMarker); err == nil && cur == status {
		return nil
	}
	if err := tm.SetPaneOption(pane, ClaudeStatusMarker, status); err != nil {
		return err
	}
	// Leaving waiting ends the needs-input episode: retire any
	// note/suggestion the arbiter parked on the row — cleanup that once
	// ran from the hub poll, now owned by the hook. Best-effort blind
	// unsets (set -pu on an absent option is fine); a miss self-heals
	// when the pane dies.
	if status != "waiting" {
		for _, m := range []string{ArbiterNoteMarker, ArbiterSuggestMarker} {
			tm.UnsetPaneOption(pane, m)
		}
	}
	// Identity rides along on every status change too, not just
	// SessionStart: every payload carries session_id/cwd, so if the
	// one-shot SessionStart write failed, the pane's identity (stat
	// column, Peek) heals itself at the next transition instead of
	// staying dead for the rest of the session. Skipped when identity
	// already wrote above (SessionStart with a status, e.g. "startup")
	// — no point writing it twice.
	if !identity && p.SessionID != "" {
		if err := tm.SetPaneOption(pane, ClaudeSessionMarker, p.SessionID); err != nil {
			return err
		}
		if err := tm.SetPaneOption(pane, ClaudeCWDMarker, p.CWD); err != nil {
			return err
		}
	}
	return tm.SetPaneOption(pane, ClaudeSinceMarker,
		strconv.FormatInt(now.Unix(), 10))
}

// HookStatus is hookAction's status alone, for callers outside this file
// that need to know whether an event means the pane is now waiting.
func HookStatus(p HookPayload) (string, bool, bool) {
	return hookAction(p)
}
