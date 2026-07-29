package hub

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindArbiter(t *testing.T) {
	panes := []Pane{
		{Session: "alpha", ID: "%1"},
		{Session: "arbiter", ID: "%2", Arbiter: true, ArbiterMode: "full"},
	}
	arb, ok := FindArbiter(panes)
	if !ok || arb.ID != "%2" {
		t.Fatalf("FindArbiter = %v %v, want %%2 true", arb.ID, ok)
	}
	if _, ok := FindArbiter(panes[:1]); ok {
		t.Error("found an arbiter in a list without one")
	}
}

func TestArbiterModeOf(t *testing.T) {
	if m := ArbiterModeOf(Pane{ArbiterMode: "full"}); m != ArbiterModeFull {
		t.Errorf("full pane = %q", m)
	}
	// Anything else — unset, garbage — reads as the safe mode.
	if m := ArbiterModeOf(Pane{ArbiterMode: "yolo"}); m != ArbiterModeRecommend {
		t.Errorf("garbage mode = %q, want recommend", m)
	}
	if m := ArbiterModeOf(Pane{}); m != ArbiterModeRecommend {
		t.Errorf("unset mode = %q, want recommend", m)
	}
}

func TestNudgeText(t *testing.T) {
	got := NudgeText("sprocket-v2", "recommend")
	if !strings.Contains(got, `"sprocket-v2"`) || !strings.Contains(got, "recommend") {
		t.Errorf("NudgeText = %q", got)
	}
}

func TestArbiterLastRoundTrip(t *testing.T) {
	at := time.Unix(1700000100, 0)
	s := FormatArbiterLast("2", at, "reason with | pipe\nand newline")
	last, ok := ParseArbiterLast(s)
	if !ok {
		t.Fatalf("ParseArbiterLast(%q) not ok", s)
	}
	if last.Digit != "2" || !last.At.Equal(at) {
		t.Errorf("got %+v", last)
	}
	if strings.Contains(last.Reason, "\n") {
		t.Errorf("reason kept a newline: %q", last.Reason)
	}
	if _, ok := ParseArbiterLast("garbage"); ok {
		t.Error("parsed garbage")
	}
	if _, ok := ParseArbiterLast(""); ok {
		t.Error("parsed empty")
	}
}

func TestSanitizeNote(t *testing.T) {
	got := sanitizeNote("  a\x1fb\nc\t d  ")
	if got != "ab c d" {
		t.Errorf("sanitizeNote = %q, want %q", got, "ab c d")
	}
	// A note the message box can hold survives whole — only a runaway
	// one is cut, and then it says so with the ellipsis.
	fits := strings.Repeat("x", noteMax)
	if got := sanitizeNote(fits); got != fits {
		t.Errorf("sanitizeNote cut a note of exactly noteMax runes")
	}
	long := strings.Repeat("x", noteMax*2)
	got = sanitizeNote(long)
	if r := []rune(got); len(r) != noteMax {
		t.Errorf("len = %d, want %d", len(r), noteMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("sanitizeNote = %q, want a trailing ellipsis when cut", got)
	}
	// Every control byte is stripped, not just \x1f — an unescaped
	// ESC/BEL would otherwise ride the option value into the TUI's
	// ANSI-passthrough reflow as a terminal-escape injection.
	if got := sanitizeNote("a \x1b[2Jb\x07"); got != "a [2Jb" {
		t.Errorf("sanitizeNote = %q, want %q", got, "a [2Jb")
	}
	if strings.ContainsAny(sanitizeNote("x\x08y"), "\x08") {
		t.Errorf("sanitizeNote left a backspace byte")
	}
}

// hubs is the socket's hub-session list Apply takes; it names the same
// session the nudgers below are built for, so they win the election.
var hubs = []string{"roost"}

func TestArbiterNudgerNudgesOncePerEpisode(t *testing.T) {
	f := &fakeTmux{}
	n := NewArbiterNudger(f, "roost")
	panes := []Pane{
		{Session: "alpha", ID: "%1", Status: StatusNeedsInput},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full",
			ArbiterSeen: true, Status: StatusIdle},
	}
	n.Apply(panes, hubs)
	if len(f.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(f.sent))
	}
	keys := f.sent[0]
	if len(keys) != 2 || keys[1] != "Enter" {
		t.Fatalf("sent %v, want [text Enter]", keys)
	}
	if want := NudgeText("alpha", "full"); keys[0] != want {
		t.Errorf("nudge = %q, want %q", keys[0], want)
	}
	if f.paneOpts["%1/"+ArbiterNudgedMarker] != "1" {
		t.Error("nudged marker not set")
	}
	// Second poll, marker now visible on the pane: no re-nudge.
	panes[0].ArbiterNudgedMark = true
	n.Apply(panes, hubs)
	if len(f.sent) != 1 {
		t.Errorf("re-nudged: %d messages", len(f.sent))
	}
}

func TestArbiterNudgerClearsEpisodeState(t *testing.T) {
	f := &fakeTmux{paneOpts: map[string]string{
		"%1/" + ArbiterNudgedMarker:  "1",
		"%1/" + ArbiterNoteMarker:    "asking something",
		"%1/" + ArbiterSuggestMarker: "1",
	}}
	n := NewArbiterNudger(f, "roost")
	n.Apply([]Pane{{Session: "alpha", ID: "%1", Status: StatusWorking,
		ArbiterNudgedMark: true, ArbiterNote: "asking something",
		ArbiterSuggest: "1"}}, hubs)
	if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
		t.Error("nudged marker survived leaving needs-input")
	}
	if _, ok := f.paneOpts["%1/"+ArbiterNoteMarker]; ok {
		t.Error("note survived leaving needs-input")
	}
	if _, ok := f.paneOpts["%1/"+ArbiterSuggestMarker]; ok {
		t.Error("suggestion survived leaving needs-input")
	}
}

func TestArbiterNudgerNeedsArbiterAndSkipsIt(t *testing.T) {
	// No arbiter session: needs-input panes are left alone entirely.
	f := &fakeTmux{}
	NewArbiterNudger(f, "roost").Apply([]Pane{{Session: "alpha", ID: "%1", Status: StatusNeedsInput}}, hubs)
	if len(f.sent) != 0 || len(f.paneOpts) != 0 {
		t.Error("nudged without an arbiter")
	}
	// The arbiter's own needs-input (its own permission prompt) is never
	// nudged about — that would loop.
	f = &fakeTmux{}
	NewArbiterNudger(f, "roost").Apply([]Pane{
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterSeen: true, Status: StatusNeedsInput},
	}, hubs)
	if len(f.sent) != 0 {
		t.Error("arbiter nudged about itself")
	}
	// nil tracker is a no-op, like the other trackers.
	var nn *ArbiterNudger
	nn.Apply([]Pane{{Session: "alpha", ID: "%1", Status: StatusNeedsInput}}, hubs)
}

func TestArbiterNudgerWaitsOneTickAfterLaunch(t *testing.T) {
	// Keys typed at a just-launched claude land in its composer with the
	// Enter swallowed, so the nudge is never submitted. The first poll to
	// see the arbiter only marks it; nudging starts a tick later.
	f := &fakeTmux{}
	n := NewArbiterNudger(f, "roost")
	panes := []Pane{
		{Session: "alpha", ID: "%1", Status: StatusNeedsInput},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full", Status: StatusIdle},
	}
	n.Apply(panes, hubs)
	if len(f.sent) != 0 {
		t.Fatalf("nudged a just-launched arbiter: %v", f.sent)
	}
	if f.sessionOpts["arbiter/"+ArbiterSeenMarker] != "1" {
		t.Fatal("seen marker not set on the first poll")
	}
	if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
		t.Error("episode marked as nudged while the nudge was held back")
	}
	// Next poll, marker now readable off the arbiter's session.
	panes[1].ArbiterSeen = true
	n.Apply(panes, hubs)
	if len(f.sent) != 1 {
		t.Fatalf("sent %d messages on the second poll, want 1", len(f.sent))
	}
}

func TestArbiterNudgerOnlyOneHubNudges(t *testing.T) {
	// Two hubs polling the same second both see an unmarked pane, so
	// set-before-send can't dedupe; the lexicographically first hub
	// session nudges and the rest stay quiet.
	panes := []Pane{
		{Session: "alpha", ID: "%1", Status: StatusNeedsInput},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full",
			ArbiterSeen: true, Status: StatusIdle},
	}
	both := []string{"roost-2", "roost"}
	f := &fakeTmux{}
	NewArbiterNudger(f, "roost-2").Apply(panes, both)
	if len(f.sent) != 0 {
		t.Errorf("follower hub nudged: %v", f.sent)
	}
	if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
		t.Error("follower hub marked the episode")
	}
	f = &fakeTmux{}
	NewArbiterNudger(f, "roost").Apply(panes, both)
	if len(f.sent) != 1 {
		t.Errorf("leader hub sent %d messages, want 1", len(f.sent))
	}
	// A hub whose own session carries no @coop marker still nudges —
	// better a duplicate than a socket where nobody nudges.
	f = &fakeTmux{}
	NewArbiterNudger(f, "roost").Apply(panes, nil)
	if len(f.sent) != 1 {
		t.Errorf("unmarked hub sent %d messages, want 1", len(f.sent))
	}
}

func TestArbiterNudgerUnmarksOnFailedSend(t *testing.T) {
	f := &fakeTmux{sendErr: errors.New("send failed")}
	n := NewArbiterNudger(f, "roost")
	panes := []Pane{
		{Session: "alpha", ID: "%1", Status: StatusNeedsInput},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full",
			ArbiterSeen: true, Status: StatusIdle},
	}
	n.Apply(panes, hubs)
	if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
		t.Error("nudged marker survived a failed SendKeys")
	}
}

func TestArbiterNudgerSkipsHubPanes(t *testing.T) {
	// Defense in depth: the live preview pane's screen is whatever
	// session it's previewing and can read needs-input, so the nudger
	// itself must never nudge about a hub pane even if the tui's
	// pre-filter somehow let one through.
	f := &fakeTmux{}
	n := NewArbiterNudger(f, "roost")
	panes := []Pane{
		{Session: "roost", ID: "%0", Hub: true, Status: StatusNeedsInput},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full",
			ArbiterSeen: true, Status: StatusIdle},
	}
	n.Apply(panes, hubs)
	if len(f.sent) != 0 {
		t.Error("nudged about a hub pane")
	}
	if _, ok := f.paneOpts["%0/"+ArbiterNudgedMarker]; ok {
		t.Error("marked a hub pane")
	}
}

// arbiterPanes is a socket with one target session and a full-mode
// arbiter — the baseline every gate test perturbs.
func arbiterPanes() []Pane {
	return []Pane{
		{Session: "alpha", ID: "%1", PID: 0},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full"},
	}
}

const dialogScreen = "Do you want to run go test?\n❯ 1. Yes\n  2. No\n"

func TestAnswerHappyPath(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	f := &fakeTmux{panes: arbiterPanes(), cmd: "claude", screen: dialogScreen}
	warn, err := Answer(f, AnswerReq{Session: "alpha", Digit: "1",
		Reason: "policy allows tests", Allowed: []string{"claude", "node"},
		Audit: audit, Now: time.Unix(1700000100, 0)})
	if err != nil || warn != "" {
		t.Fatalf("Answer = %q, %v", warn, err)
	}
	if len(f.sent) != 1 || len(f.sent[0]) != 1 || f.sent[0][0] != "1" {
		t.Errorf("sent %v, want the single digit", f.sent)
	}
	last, ok := ParseArbiterLast(f.paneOpts["%1/"+ArbiterLastMarker])
	if !ok || last.Digit != "1" || last.Reason != "policy allows tests" {
		t.Errorf("marker = %+v %v", last, ok)
	}
	raw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"action":"answered"`) ||
		!strings.Contains(string(raw), "Do you want to run go test?") {
		t.Errorf("audit = %s", raw)
	}
}

func TestAnswerGates(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	base := func() AnswerReq {
		return AnswerReq{Session: "alpha", Digit: "1", Reason: "r",
			Allowed: []string{"claude"}, Audit: audit, Now: time.Unix(1700000100, 0)}
	}
	cases := []struct {
		name string
		tm   *fakeTmux
		req  AnswerReq
		want string
	}{
		{"bad digit", &fakeTmux{panes: arbiterPanes(), cmd: "claude", screen: dialogScreen},
			AnswerReq{Session: "alpha", Digit: "12", Reason: "r", Allowed: []string{"claude"}, Audit: audit}, "digit"},
		{"no reason", &fakeTmux{panes: arbiterPanes(), cmd: "claude", screen: dialogScreen},
			AnswerReq{Session: "alpha", Digit: "1", Allowed: []string{"claude"}, Audit: audit}, "reason"},
		{"unknown session", &fakeTmux{panes: arbiterPanes(), cmd: "claude", screen: dialogScreen},
			func() AnswerReq { r := base(); r.Session = "ghost"; return r }(), "no session"},
		{"hub target", &fakeTmux{panes: []Pane{{Session: "roost", ID: "%0", Hub: true},
			{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full"}}, cmd: "claude", screen: dialogScreen},
			func() AnswerReq { r := base(); r.Session = "roost"; return r }(), "coop's own"},
		{"arbiter target", &fakeTmux{panes: arbiterPanes(), cmd: "claude", screen: dialogScreen},
			func() AnswerReq { r := base(); r.Session = "arbiter"; return r }(), "coop's own"},
		{"no arbiter", &fakeTmux{panes: arbiterPanes()[:1], cmd: "claude", screen: dialogScreen},
			base(), "no arbiter"},
		{"recommend mode", &fakeTmux{panes: []Pane{{Session: "alpha", ID: "%1"},
			{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "recommend"}},
			cmd: "claude", screen: dialogScreen}, base(), "recommend-only"},
		{"disallowed cmd", &fakeTmux{panes: arbiterPanes(), cmd: "bash", screen: dialogScreen},
			base(), "refusing"},
		{"no dialog", &fakeTmux{panes: arbiterPanes(), cmd: "claude", screen: "just chatting\n"},
			base(), "no open dialog"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Answer(c.tm, c.req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want substring %q", err, c.want)
			}
			if len(c.tm.sent) != 0 {
				t.Errorf("refused answer still sent keys: %v", c.tm.sent)
			}
		})
	}
}

func TestNote(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	f := &fakeTmux{panes: arbiterPanes()}
	warn, err := Note(f, NoteReq{Session: "alpha", Text: "asking to drop a table\n— suggest 2",
		Audit: audit, Now: time.Unix(1700000100, 0)})
	if err != nil || warn != "" {
		t.Fatalf("Note = %q, %v", warn, err)
	}
	got := f.paneOpts["%1/"+ArbiterNoteMarker]
	if strings.Contains(got, "\n") || !strings.Contains(got, "suggest 2") {
		t.Errorf("note option = %q", got)
	}
	raw, _ := os.ReadFile(audit)
	if !strings.Contains(string(raw), `"action":"escalated"`) {
		t.Errorf("audit = %s", raw)
	}
	if _, err := Note(f, NoteReq{Session: "arbiter", Text: "x", Audit: audit}); err == nil {
		t.Error("noted the arbiter itself")
	}
}

// The suggestion is its own option, and an unsuggested note clears any
// digit an earlier one left — the row must never offer a stale answer
// under fresh text.
func TestNoteSuggest(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	f := &fakeTmux{panes: arbiterPanes()}
	if _, err := Note(f, NoteReq{Session: "alpha", Text: "asking to run tests",
		Suggest: "2", Audit: audit, Now: time.Unix(1700000100, 0)}); err != nil {
		t.Fatalf("Note = %v", err)
	}
	if got := f.paneOpts["%1/"+ArbiterSuggestMarker]; got != "2" {
		t.Errorf("suggest option = %q, want 2", got)
	}
	raw, _ := os.ReadFile(audit)
	if !strings.Contains(string(raw), `"suggest":"2"`) {
		t.Errorf("audit = %s", raw)
	}
	if _, err := Note(f, NoteReq{Session: "alpha", Text: "now asking something else",
		Audit: audit, Now: time.Unix(1700000200, 0)}); err != nil {
		t.Fatalf("Note = %v", err)
	}
	if _, ok := f.paneOpts["%1/"+ArbiterSuggestMarker]; ok {
		t.Error("suggestion survived a note that named none")
	}

	// A bad digit is refused before anything is written.
	f = &fakeTmux{panes: arbiterPanes()}
	if _, err := Note(f, NoteReq{Session: "alpha", Text: "t", Suggest: "12",
		Audit: audit}); err == nil || !strings.Contains(err.Error(), "single 0-9") {
		t.Errorf("err = %v, want a suggest-shape refusal", err)
	}
	if len(f.paneOpts) != 0 {
		t.Errorf("refused note still wrote %v", f.paneOpts)
	}
}

// The option is only a tmux option; anything but a single digit is
// ignored rather than handed to send-keys.
func TestArbiterSuggestOf(t *testing.T) {
	for in, want := range map[string]string{
		"1": "1", "0": "0", "": "", "12": "", "Enter": "", " 1": "",
	} {
		if got := ArbiterSuggestOf(Pane{ArbiterSuggest: in}); got != want {
			t.Errorf("ArbiterSuggestOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPeek(t *testing.T) {
	f := &fakeTmux{panes: arbiterPanes(), screen: "\x1b[1mDo it?\x1b[0m\n❯ 1. Yes\n"}
	out, err := Peek(f, &Transcripts{}, nil, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Do it?") || strings.Contains(out, "\x1b[") {
		t.Errorf("Peek = %q, want stripped screen", out)
	}
	if _, err := Peek(f, &Transcripts{}, nil, "ghost"); err == nil {
		t.Error("peeked a missing session")
	}
}

func TestArbiterHomeSeedsOnce(t *testing.T) {
	cfg := t.TempDir()
	dir, err := ArbiterHome(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(cfg, "arbiter") {
		t.Errorf("dir = %q", dir)
	}
	settings, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Bash(coop peek:*)"`, `"Bash(coop answer:*)"`, `"Bash(coop note:*)"`} {
		if !strings.Contains(string(settings), want) {
			t.Errorf("settings = %s, want %s", settings, want)
		}
	}
	policyPath := filepath.Join(cfg, "arbiter.md")
	if _, err := os.Stat(policyPath); err != nil {
		t.Fatal(err)
	}
	// Existing files are the user's — a second call must not overwrite.
	if err := os.WriteFile(policyPath, []byte("my rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArbiterHome(cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(policyPath)
	if string(got) != "my rules" {
		t.Error("ArbiterHome overwrote an existing policy")
	}
}

func TestArbiterCmdQuoting(t *testing.T) {
	cmd := ArbiterCmd("claude,node", "claude", "sonnet", "don't approve pushes")
	if !strings.HasPrefix(cmd, "COOP_ALLOWED_CMDS='claude,node' claude --model 'sonnet' --append-system-prompt '") {
		t.Errorf("cmd = %q", cmd)
	}
	// The policy's single quote must survive shell parsing: ' -> '\''
	if !strings.Contains(cmd, `don'\''t`) {
		t.Errorf("quote not escaped: %q", cmd)
	}
}

func TestLaunchArbiter(t *testing.T) {
	f := &fakeTmux{}
	cfg := t.TempDir()
	if err := LaunchArbiter(f, cfg, "claude,node", "claude", "sonnet", 120, 40); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %d sessions", len(f.created))
	}
	c := f.created[0]
	if c[0] != ArbiterSession || c[1] != filepath.Join(cfg, "arbiter") {
		t.Errorf("created = %v", c)
	}
	if !strings.Contains(c[2], "COOP_ALLOWED_CMDS='claude,node'") {
		t.Errorf("cmd missing allowed-cmds env prefix: %q", c[2])
	}
	if !strings.Contains(c[2], "--append-system-prompt") {
		t.Errorf("cmd = %q", c[2])
	}
	if f.sessionOpts[ArbiterSession+"/"+ArbiterMarker] != "1" {
		t.Error("arbiter marker not set")
	}
	if f.sessionOpts[ArbiterSession+"/"+ArbiterModeMarker] != ArbiterModeRecommend {
		t.Error("mode not seeded to recommend")
	}
}
