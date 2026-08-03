package hub

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildJudgePrompt(t *testing.T) {
	got := buildJudgePrompt("sprocket-v2", "Bash: git push origin main",
		"May I run this?\n 1. Yes\n 2. No", "I need to push the branch.")
	want := `session: "sprocket-v2"
trigger: Bash: git push origin main

=== screen ===
May I run this?
 1. Yes
 2. No

=== last assistant message ===
I need to push the branch.
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestBuildJudgePromptOmitsEmptySections(t *testing.T) {
	got := buildJudgePrompt("alpha", "", "dialog", "")
	want := `session: "alpha"

=== screen ===
dialog
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name    string
		result  string
		want    Verdict
		wantErr bool
	}{
		{"bare", `{"action":"answer","digit":"1","reason":"runs the tests"}`,
			Verdict{VerdictAnswer, "1", "runs the tests"}, false},
		{"fenced", "```json\n{\"action\":\"escalate\",\"digit\":\"2\",\"reason\":\"pushes to remote\"}\n```",
			Verdict{VerdictEscalate, "2", "pushes to remote"}, false},
		{"prose-wrapped", "Here is my verdict:\n{\"action\":\"escalate\",\"reason\":\"unclear\"}\nHope that helps.",
			Verdict{VerdictEscalate, "", "unclear"}, false},
		{"no digit on escalate", `{"action":"escalate","reason":"no numbered dialog"}`,
			Verdict{VerdictEscalate, "", "no numbered dialog"}, false},
		{"malformed", "I couldn't decide.", Verdict{}, true},
		{"unknown action", `{"action":"panic","digit":"1","reason":"x"}`, Verdict{}, true},
		{"non-digit digit", `{"action":"answer","digit":"12","reason":"x"}`, Verdict{}, true},
		{"answer with no digit", `{"action":"answer","reason":"x"}`, Verdict{}, true},
		{"empty reason", `{"action":"escalate","digit":"1","reason":"  "}`, Verdict{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envelope := `{"type":"result","is_error":false,"result":` +
				mustJSONString(tc.result) + `}`
			got, err := parseVerdict(envelope)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseVerdictRejectsBadEnvelope(t *testing.T) {
	if _, err := parseVerdict("not json at all"); err == nil {
		t.Error("want error on a non-JSON envelope")
	}
}

func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// episodeSince is the fixture episode: the @coop_status_since both the
// pane list and the option Answer re-reads at send time carry.
const episodeSince = 1700000000

func waitingPane() Pane {
	return Pane{
		ID: "%1", Session: "sprocket-v2",
		Claude: &ClaudeState{Status: "waiting", StatusSince: time.Unix(episodeSince, 0),
			SessionID: "sess-1", CWD: "/home/user/sprocket-v2"},
	}
}

func judgeFake(mode string) *fakeTmux {
	f := &fakeTmux{
		panes:   []Pane{waitingPane()},
		globals: map[string]string{ArbiterModeMarker: mode},
		// Needs the ❯ caret NeedsInputScreen's regexp requires on the
		// selected option — a fixture without it fails Answer's dialog
		// check before ever exercising the allowlist gate.
		screen: "May I run the tests?\n❯ 1. Yes\n 2. No",
		cmd:    "claude",
	}
	seedSince(f, "%1")
	return f
}

func seedSince(f *fakeTmux, panes ...string) {
	for _, p := range panes {
		f.SetPaneOption(p, ClaudeSinceMarker, strconv.FormatInt(episodeSince, 10))
	}
}

func runOK(result string) func(string) (string, error) {
	return func(string) (string, error) {
		return `{"is_error":false,"result":` + mustJSONString(result) + `}`, nil
	}
}

func baseReq(run func(string) (string, error)) JudgeReq {
	return JudgeReq{PaneID: "%1", Detail: "Bash: go test ./...",
		Allowed: []string{"claude"}, Audit: "",
		Now: func() time.Time { return time.Unix(1700000500, 0) }, Run: run}
}

func TestJudgeFullModeAnswers(t *testing.T) {
	f := judgeFake(ArbiterModeFull)
	req := baseReq(runOK(`{"action":"answer","digit":"1","reason":"runs the tests"}`))
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || len(f.sent[0]) == 0 || f.sent[0][0] != "1" {
		t.Errorf("want digit 1 sent, got %v", f.sent)
	}
	if f.paneOpts["%1/"+ArbiterNoteMarker] != "" {
		t.Error("an accepted answer should not also leave a note")
	}
}

func TestJudgeRecommendModeDowngradesToNoteWithSuggest(t *testing.T) {
	f := judgeFake(ArbiterModeRecommend)
	req := baseReq(runOK(`{"action":"answer","digit":"1","reason":"runs the tests"}`))
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 0 {
		t.Errorf("recommend mode must never send keys, got %v", f.sent)
	}
	if got := f.paneOpts["%1/"+ArbiterNoteMarker]; got != "runs the tests" {
		t.Errorf("note = %q", got)
	}
	if got := f.paneOpts["%1/"+ArbiterSuggestMarker]; got != "1" {
		t.Errorf("suggest = %q, want 1", got)
	}
}

func TestJudgeEscalateNotesWithoutSending(t *testing.T) {
	f := judgeFake(ArbiterModeFull)
	req := baseReq(runOK(`{"action":"escalate","digit":"2","reason":"pushes to a remote"}`))
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 0 {
		t.Error("escalate must not send keys")
	}
	if got := f.paneOpts["%1/"+ArbiterSuggestMarker]; got != "2" {
		t.Errorf("suggest = %q, want 2", got)
	}
}

func TestJudgeFallsBackToNoteWhenAnswerRefused(t *testing.T) {
	f := judgeFake(ArbiterModeFull)
	f.cmd = "zsh" // dead claude — Answer's allowlist gate refuses
	req := baseReq(runOK(`{"action":"answer","digit":"1","reason":"runs the tests"}`))
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 0 {
		t.Error("a refused answer must not send keys")
	}
	note := f.paneOpts["%1/"+ArbiterNoteMarker]
	if !strings.Contains(note, "runs the tests") || !strings.Contains(note, "refused") {
		t.Errorf("note should carry both the reason and the refusal, got %q", note)
	}
}

// A session whose window is split has two panes sharing session_name.
// The judge reasoned about one of them, so the digit and the note go to
// that pane id — resolving by name again would answer the wrong pane,
// and Note (which has no screen gate) would park the suggestion there.
func TestJudgeTargetsThePaneItJudged(t *testing.T) {
	for _, tc := range []struct{ name, verdict, opt string }{
		{"answer", `{"action":"answer","digit":"1","reason":"runs the tests"}`, ArbiterLastMarker},
		{"escalate", `{"action":"escalate","digit":"1","reason":"unclear"}`, ArbiterNoteMarker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := judgeFake(ArbiterModeFull)
			// The judged pane is the second in the same session.
			f.panes = []Pane{
				{ID: "%4", Session: "sprocket-v2"},
				func() Pane { p := waitingPane(); p.ID = "%5"; return p }(),
			}
			seedSince(f, "%5")
			req := baseReq(runOK(tc.verdict))
			req.PaneID = "%5"
			if err := Judge(f, nil, req); err != nil {
				t.Fatal(err)
			}
			if f.paneOpts["%4/"+tc.opt] != "" {
				t.Errorf("%s landed on %%4, the first pane of the session", tc.opt)
			}
			if f.paneOpts["%5/"+tc.opt] == "" {
				t.Errorf("%s never reached the judged pane", tc.opt)
			}
		})
	}
}

// The episode the judge inspected has to survive the model turn. A
// dialog answered by the human and replaced by another while claude -p
// was running passes every other gate; the note it falls back to is what
// the operator sees instead of a digit in the wrong dialog.
func TestJudgeRefusesWhenTheEpisodeMovedOn(t *testing.T) {
	f := judgeFake(ArbiterModeFull)
	req := baseReq(func(string) (string, error) {
		// The human answers and claude opens a different dialog during
		// the turn: @coop_status_since moves, the screen still shows a
		// numbered dialog.
		seedSince(f, "%1")
		f.SetPaneOption("%1", ClaudeSinceMarker, "1700000600")
		return `{"is_error":false,"result":` +
			mustJSONString(`{"action":"answer","digit":"1","reason":"runs the tests"}`) + `}`, nil
	})
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 0 {
		t.Errorf("digit landed in a dialog the judge never saw: %v", f.sent)
	}
	if note := f.paneOpts["%1/"+ArbiterNoteMarker]; !strings.Contains(note, "different dialog") {
		t.Errorf("note should carry the refusal, got %q", note)
	}
}

// The audit line and the row's "answered N ago" are stamped when the
// action lands, not when the episode opened: a model turn is bounded
// only by judgeTimeout, so an up-front stamp dates both up to a minute
// early.
func TestJudgeStampsTheActionNotTheStart(t *testing.T) {
	f := judgeFake(ArbiterModeFull)
	req := baseReq(runOK(`{"action":"answer","digit":"1","reason":"runs the tests"}`))
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	last, ok := ParseArbiterLast(f.paneOpts["%1/"+ArbiterLastMarker])
	if !ok || !last.At.Equal(time.Unix(1700000500, 0)) {
		t.Errorf("marker at %v, want the time Now returned at apply", last.At)
	}
}

func TestJudgeSkipsWhenModeOff(t *testing.T) {
	f := judgeFake(ArbiterModeOff)
	called := false
	req := baseReq(func(string) (string, error) { called = true; return "", nil })
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("mode off must not spend a claude invocation")
	}
}

func TestJudgeSkipsWhenNoLongerWaiting(t *testing.T) {
	f := judgeFake(ArbiterModeFull)
	f.panes[0].Claude.Status = "busy" // the human answered first
	called := false
	req := baseReq(func(string) (string, error) { called = true; return "", nil })
	if err := Judge(f, nil, req); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("a pane that stopped waiting must not be judged")
	}
}

func TestJudgeMalformedVerdictLeavesRowAlone(t *testing.T) {
	f := judgeFake(ArbiterModeFull)
	req := baseReq(runOK("I could not decide."))
	if err := Judge(f, nil, req); err == nil {
		t.Fatal("want an error for the log")
	}
	if len(f.sent) != 0 || f.paneOpts["%1/"+ArbiterNoteMarker] != "" {
		t.Error("a malformed verdict must leave the row untouched")
	}
}
