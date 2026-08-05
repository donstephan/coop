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
// and claude -p returns a verdict coop applies through Note.
// Nothing the model produces is a command — the verdict is data this
// package validates before any of it reaches a gate.

// Verdict actions.
const (
	VerdictAnswer   = "answer"
	VerdictEscalate = "escalate"
)

// Verdict is one judgement: what the model would do about a dialog, not
// what coop does about it. The arbiter has no send path, so an answer
// always lands as a note carrying the digit as a suggestion, and the two
// actions differ only in how confident the model was — see applyVerdict
// for why the prompt is still asked for both.
type Verdict struct {
	Action string `json:"action"` // VerdictAnswer | VerdictEscalate
	Digit  string `json:"digit"`  // single 0-9, "" when no option is named
	Reason string `json:"reason"`
}

// buildJudgePrompt renders one episode for the model. Sections are
// omitted rather than left empty so a missing transcript doesn't read as
// an assistant that said nothing. Everything here is untrusted data from
// the monitored session — the preamble (arbiterPreamble) is what says so.
//
// agent is the subagent the request came from ("" for the main thread),
// and it changes what the last assistant message *means*: LastText skips
// sidechain turns, so for a subagent's dialog that message is the
// parent's narration ("dispatching review") and not a word about what
// the subagent asked for. It stays in the prompt — the main thread's
// plan is the context the subagent was dispatched under — but under a
// heading that says whose it is, because presented unlabelled it reads
// as an explanation of this request and the judge escalates on
// "unclear what that entails" every time. The name is sanitized and
// quoted like every other untrusted field: it comes from the payload,
// and a plausible-looking instruction wearing an agent_type is still
// data.
func buildJudgePrompt(session, detail, screen, lastMsg, agent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session: %q\n", session)
	if agent = sanitizeNote(agent); agent != "" {
		fmt.Fprintf(&b, "requested by: %q subagent of this session\n", agent)
	}
	if detail = sanitizeNote(detail); detail != "" {
		fmt.Fprintf(&b, "trigger: %s\n", detail)
	}
	fmt.Fprintf(&b, "\n=== screen ===\n%s\n", strings.TrimRight(screen, "\n"))
	if lastMsg = strings.TrimSpace(lastMsg); lastMsg != "" {
		if agent != "" {
			fmt.Fprintf(&b, "\n=== last assistant message (the main thread's, not the %q subagent that made this request) ===\n%s\n",
				agent, lastMsg)
		} else {
			fmt.Fprintf(&b, "\n=== last assistant message ===\n%s\n", lastMsg)
		}
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
	PaneID string
	Detail string // NudgeDetail of the triggering event, "" when there was none
	// Agent is HookAgent of the triggering event: the subagent that
	// asked, "" for the main thread. It only ever labels the prompt —
	// no gate reads it.
	Agent string
	Audit string // audit log path
	// Now is read at the moment an action lands, not when the episode
	// started: a model turn is bounded only by judgeTimeout, so a time
	// stamped up front would date every audit line up to a minute early.
	// nil means time.Now.
	Now func() time.Time
	// Wait is the pause between capture attempts while the dialog paints
	// (see captureDialog). nil means time.Sleep; tests pass a counter so
	// the bound is asserted without spending the wall clock on it.
	Wait func(time.Duration)
	Run  func(prompt string) (string, error)
	Log  func(format string, args ...any)
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

func (r JudgeReq) wait(d time.Duration) {
	if r.Wait != nil {
		r.Wait(d)
		return
	}
	time.Sleep(d)
}

// The judge is spawned from the hook that announces the dialog, and
// PermissionRequest fires *before* Claude Code renders one — a hook may
// decide the permission itself, so the paint waits for every hook to
// return. A capture taken the moment the judge starts is therefore
// racing a screen that has not been drawn yet, and losing that race is
// not a stale row: the model is handed a real trigger beside a screen
// with no dialog on it and escalates with "no numbered dialog is visible
// on screen to confirm", which reads as a judgement about the command.
//
// So the capture retries. Each attempt is one capture-pane, and the
// whole bound is a fraction of the model turn it precedes, so the wait
// costs nothing next to the call it is protecting.
const (
	dialogAttempts = 6
	dialogDelay    = 200 * time.Millisecond
)

// captureDialog captures the pane, retrying until the dialog is on
// screen or the attempts run out. ok is false when the episode ended
// while waiting.
//
// A dialog that never arrives is still judged: an escalation over a
// screen that genuinely showed none is the honest outcome, and the
// episode's dialog= line is what tells that apart from a verdict
// blaming the screen. Only an answered dialog is dropped.
func captureDialog(tm Tmux, pane string, req JudgeReq) (screen string, ok bool, err error) {
	for i := 0; ; i++ {
		if screen, err = tm.CapturePane(pane); err != nil {
			return "", false, err
		}
		if NeedsInputScreen(screen) || i == dialogAttempts-1 {
			return screen, true, nil
		}
		req.wait(dialogDelay)
		// The human now has the whole wait to answer in, and judging a
		// dialog they have moved past spends a model turn to park a note
		// on a row ApplyHook has already retired. Re-read the one option
		// that says so rather than the whole pane list. Only a status
		// that positively says otherwise ends the wait: a failed read and
		// an unset option are both "don't know", and reading either as an
		// answer would abandon every episode after one attempt — which is
		// the whole race, restored.
		if s, err := tm.PaneOption(pane, ClaudeStatusMarker); err == nil && s != "" && s != "waiting" {
			return "", false, nil
		}
	}
}

// Judge runs one episode end to end: re-check the mode and the pane,
// gather context, ask the model, apply the verdict. Every exit is
// deliberately quiet — a returned error is something for the judge log,
// never something that changes what the operator sees. A row that gets
// no note just reads "waiting", which is true.
func Judge(tm Tmux, tr *Transcripts, req JudgeReq) error {
	if ArbiterMode(tm) == ArbiterModeOff {
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
	screen, ok, err := captureDialog(tm, p.ID, req)
	if err != nil {
		return err
	}
	if !ok {
		return nil // answered while the dialog was still painting
	}
	last := ""
	if c := p.Claude; tr != nil && c.SessionID != "" {
		if text, ok := tr.LastText(c.SessionID, c.CWD); ok {
			last = text
		}
	}
	plain := StripANSI(screen)
	prompt := buildJudgePrompt(p.Session, req.Detail, plain, last, req.Agent)
	out, err := req.Run(prompt)
	if err != nil {
		return err
	}
	v, err := parseVerdict(out)
	if err != nil {
		req.logf("verdict: %v (raw: %q)", err, out)
		return err
	}
	// One line per episode, whatever the outcome — the judge log used to
	// hold failures only, and "escalated: cannot see the dialog" was then
	// indistinguishable from a capture that genuinely showed none. dialog
	// is NeedsInputScreen's own verdict on the same capture, so a model
	// blaming the screen can be checked against what that saw.
	// Deliberately a summary and not the screen itself: capture output is
	// untrusted and a terminal's worth of it per episode would bury the
	// log it is meant to make readable.
	req.logf("episode %s %q: dialog=%v agent=%q verdict=%s (%s)",
		p.ID, p.Session, NeedsInputScreen(plain), sanitizeNote(req.Agent),
		v.Action, sanitizeNote(v.Reason))
	return applyVerdict(tm, *p, v, req)
}

// applyVerdict lands a verdict on the pane's row. Every verdict is a
// note: the arbiter has no send path, so an "answer" and an "escalate"
// differ only in how confident the model was, and both leave the digit
// as the suggestion the space key applies. The prompt still offers both
// actions on purpose — the model is asked what it would do, and coop
// decides what that is worth, which is one less thing the prompt can get
// wrong.
//
// It goes to p.ID, the pane Judge validated, never to p.Session: a
// session whose window is split has two panes sharing session_name, and
// the note would otherwise park on whichever tmux listed first.
func applyVerdict(tm Tmux, p Pane, v Verdict, req JudgeReq) error {
	warn, err := Note(tm, NoteReq{PaneID: p.ID, Text: v.Reason,
		Suggest: v.Digit, Audit: req.Audit, Now: req.now()})
	if warn != "" {
		req.logf("note on %s: %s", p.Session, warn)
	}
	return err
}
