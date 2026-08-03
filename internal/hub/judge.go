package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The arbiter is a one-off headless claude per needs-input episode, not
// a session: coop hook spawns "coop judge <pane>" right after it
// publishes waiting, the judge builds a prompt from the pane's screen,
// and claude -p returns a verdict coop applies through Answer/Note.
// Nothing the model produces is a command — the verdict is data this
// package validates before any of it reaches a gate.

// Verdict actions.
const (
	VerdictAnswer   = "answer"
	VerdictEscalate = "escalate"
)

// Verdict is one judgement: what the model would do about a dialog. It
// deliberately does not carry the mode — the model is asked what it
// would do, and coop degrades an answer to a note-with-suggestion when
// the mode is recommend. One less thing the prompt can get wrong.
type Verdict struct {
	Action string `json:"action"` // VerdictAnswer | VerdictEscalate
	Digit  string `json:"digit"`  // single 0-9, "" when no option is named
	Reason string `json:"reason"`
}

// buildJudgePrompt renders one episode for the model. Sections are
// omitted rather than left empty so a missing transcript doesn't read as
// an assistant that said nothing. Everything here is untrusted data from
// the monitored session — the preamble (arbiterPreamble) is what says so.
func buildJudgePrompt(session, detail, screen, lastMsg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session: %q\n", session)
	if detail = sanitizeNote(detail); detail != "" {
		fmt.Fprintf(&b, "trigger: %s\n", detail)
	}
	fmt.Fprintf(&b, "\n=== screen ===\n%s\n", strings.TrimRight(screen, "\n"))
	if lastMsg = strings.TrimSpace(lastMsg); lastMsg != "" {
		fmt.Fprintf(&b, "\n=== last assistant message ===\n%s\n", lastMsg)
	}
	return b.String()
}

// judgeEnvelope is claude -p --output-format json's wrapper. Only the
// result text matters; the verdict is parsed out of it.
type judgeEnvelope struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// parseVerdict pulls a verdict out of one claude -p invocation. Two
// layers: the --output-format json envelope, then the first complete
// JSON object inside the result text (the model may fence it or wrap it
// in prose). Every field is re-validated here rather than at the gate,
// because a malformed verdict must be a no-op, not a wrong send.
func parseVerdict(out string) (Verdict, error) {
	var env judgeEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		return Verdict{}, fmt.Errorf("claude output is not a json envelope: %w", err)
	}
	if env.IsError {
		return Verdict{}, fmt.Errorf("claude reported an error: %s", env.Result)
	}
	raw, ok := firstJSONObject(env.Result)
	if !ok {
		return Verdict{}, fmt.Errorf("no json object in verdict: %q", env.Result)
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return Verdict{}, fmt.Errorf("verdict: %w", err)
	}
	if v.Action != VerdictAnswer && v.Action != VerdictEscalate {
		return Verdict{}, fmt.Errorf("unknown action %q", v.Action)
	}
	if v.Digit != "" && !digitRe.MatchString(v.Digit) {
		return Verdict{}, fmt.Errorf("digit must be a single 0-9, got %q", v.Digit)
	}
	if v.Action == VerdictAnswer && v.Digit == "" {
		return Verdict{}, fmt.Errorf("action answer with no digit")
	}
	if strings.TrimSpace(v.Reason) == "" {
		return Verdict{}, fmt.Errorf("a reason is required")
	}
	return v, nil
}

// firstJSONObject returns the first complete JSON object in s. The
// decoder stops at the end of one value, so a fenced block or trailing
// prose costs nothing.
func firstJSONObject(s string) (json.RawMessage, bool) {
	i := strings.IndexByte(s, '{')
	if i < 0 {
		return nil, false
	}
	var raw json.RawMessage
	if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&raw); err != nil {
		return nil, false
	}
	return raw, true
}

// JudgeReq is one judging episode. Run is injected so tests never spawn
// a process; Log is where diagnostics go (nil for none).
type JudgeReq struct {
	PaneID  string
	Detail  string   // NudgeDetail of the triggering event, "" when there was none
	Allowed []string // pane_current_command allowlist, same gate as the TUI's
	Audit   string   // audit log path
	// Now is read at the moment an action lands, not when the episode
	// started: a model turn is bounded only by judgeTimeout, so a time
	// stamped up front would date every audit line and every "answered
	// N ago" up to a minute early. nil means time.Now.
	Now func() time.Time
	Run func(prompt string) (string, error)
	Log func(format string, args ...any)
}

func (r JudgeReq) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r JudgeReq) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Judge runs one episode end to end: re-check the mode and the pane,
// gather context, ask the model, apply the verdict. Every exit is
// deliberately quiet — a returned error is something for the judge log,
// never something that changes what the operator sees. A row that gets
// no note just reads "waiting", which is true.
func Judge(tm Tmux, tr *Transcripts, req JudgeReq) error {
	mode := ArbiterMode(tm)
	if mode == ArbiterModeOff {
		return nil // turned off between the hook firing and this process starting
	}
	panes, err := tm.ListSessions()
	if err != nil {
		return err
	}
	var p *Pane
	for i := range panes {
		if panes[i].ID == req.PaneID {
			p = &panes[i]
			break
		}
	}
	if p == nil {
		return fmt.Errorf("pane %s is gone", req.PaneID)
	}
	if p.Hub {
		return fmt.Errorf("refusing to judge %q: coop's own session", p.Session)
	}
	// The dialog may already be answered: the human is faster than a
	// process start plus a model turn more often than you'd think.
	if p.Claude == nil || p.Claude.Status != "waiting" {
		return nil
	}
	// The episode this verdict will be about. Answer re-reads it at send
	// time and refuses on a mismatch, so a dialog answered and replaced
	// during the model turn is never the one that receives the digit.
	since := p.Claude.StatusSince
	screen, err := tm.CapturePane(p.ID)
	if err != nil {
		return err
	}
	last := ""
	if c := p.Claude; tr != nil && c.SessionID != "" {
		if text, ok := tr.LastText(c.SessionID, c.CWD); ok {
			last = text
		}
	}
	prompt := buildJudgePrompt(p.Session, req.Detail, StripANSI(screen), last)
	out, err := req.Run(prompt)
	if err != nil {
		return err
	}
	v, err := parseVerdict(out)
	if err != nil {
		req.logf("verdict: %v (raw: %q)", err, out)
		return err
	}
	return applyVerdict(tm, *p, since, mode, v, req)
}

// applyVerdict routes a verdict through the same gates the helper CLI
// used to defend. Full mode plus an answer is the only path that sends a
// key; everything else — recommend mode, an escalation, or an Answer the
// gates refused — lands as a note, keeping the digit as the suggestion
// the space key applies.
//
// Both go to p.ID, the pane Judge validated, never to p.Session: a
// session whose window is split has two panes sharing session_name, and
// the note (which has no screen gate at all) would otherwise park on
// whichever tmux listed first.
func applyVerdict(tm Tmux, p Pane, since time.Time, mode string, v Verdict, req JudgeReq) error {
	text := v.Reason
	if mode == ArbiterModeFull && v.Action == VerdictAnswer {
		warn, err := Answer(tm, AnswerReq{PaneID: p.ID, Digit: v.Digit,
			Reason: v.Reason, Allowed: req.Allowed, Audit: req.Audit,
			Now: req.now(), Since: since})
		if err == nil {
			if warn != "" {
				req.logf("answered %s with %s (%s)", p.Session, v.Digit, warn)
			}
			return nil
		}
		// The gates refused after the model committed — the screen
		// changed, or the pane is a dead claude's shell. Escalate with
		// the refusal attached rather than retrying: whatever the gate
		// saw, the human should see too.
		req.logf("answer refused for %s: %v", p.Session, err)
		text = v.Reason + " (answer refused: " + err.Error() + ")"
	}
	warn, err := Note(tm, NoteReq{PaneID: p.ID, Text: text,
		Suggest: v.Digit, Audit: req.Audit, Now: req.now()})
	if warn != "" {
		req.logf("note on %s: %s", p.Session, warn)
	}
	return err
}
