package hub

import (
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
	got := NudgeText("sprocket-v2", "recommend", "")
	if !strings.Contains(got, `"sprocket-v2"`) || !strings.Contains(got, "recommend") {
		t.Errorf("NudgeText = %q", got)
	}
}

func TestNudgeTextDetail(t *testing.T) {
	if got := NudgeText("alpha", "recommend", ""); got != `coop: session "alpha" needs input (mode: recommend)` {
		t.Errorf("plain form changed: %q", got)
	}
	if got := NudgeText("alpha", "full", "Bash: npm test"); got != `coop: session "alpha" needs input (mode: full; trigger: Bash: npm test)` {
		t.Errorf("detail form: %q", got)
	}
	// Control bytes and newlines in the detail are typed into a live
	// claude pane — they must be flattened, and the whole line capped.
	got := NudgeText("alpha", "full", "Bash: rm\n-rf \x1b[31m"+strings.Repeat("x", 400))
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("detail not sanitized: %q", got)
	}
	if n := len([]rune(got)); n > 220 {
		t.Errorf("nudge line %d runes, want ≤220", n)
	}
}

func TestCatchupNudge(t *testing.T) {
	arb := Pane{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full"}
	panes := []Pane{
		// hook-published waiting, un-nudged: gets the catch-up
		{Session: "alpha", ID: "%1", Claude: &ClaudeState{Status: "waiting"}},
		// already nudged: skipped
		{Session: "beta", ID: "%2", Claude: &ClaudeState{Status: "waiting"}, ArbiterNudgedMark: true},
		// busy: skipped
		{Session: "gamma", ID: "%3", Claude: &ClaudeState{Status: "busy"}},
		// title-tier needs-input with no hook state: skipped — nothing
		// would ever clear a marker set here
		{Session: "delta", ID: "%4", Title: "🔔 pick one"},
		arb,
		{Session: "roost", ID: "%0", Hub: true},
	}
	f := &fakeTmux{}
	CatchupNudge(f, panes, arb)
	if len(f.sent) != 1 {
		t.Fatalf("sent = %v, want exactly alpha's nudge", f.sent)
	}
	if want := `coop: session "alpha" needs input (mode: full)`; f.sent[0][0] != want {
		t.Errorf("nudge = %q, want %q", f.sent[0][0], want)
	}
	if f.paneOpts["%1/"+ArbiterNudgedMarker] != "1" {
		t.Error("alpha's marker not set")
	}
	if _, ok := f.paneOpts["%4/"+ArbiterNudgedMarker]; ok {
		t.Error("title-tier pane acquired a marker")
	}
}

// RetireStaleEpisodes is the non-hook counterpart to ApplyHook's
// waiting-exit cleanup: it is the only thing that ever clears markers
// on a pane with no hook state, so a stale suggest digit from a
// long-closed dialog doesn't survive to be applied with space.
func TestRetireStaleEpisodes(t *testing.T) {
	panes := []Pane{
		// idle, no hook state: every marker retires
		{Session: "alpha", ID: "%1", Status: StatusIdle,
			ArbiterNudgedMark: true, ArbiterNote: "old note", ArbiterSuggest: "2"},
		// hook pane (Claude set): ApplyHook owns its cleanup, untouched here
		{Session: "beta", ID: "%2", Status: StatusIdle, Claude: &ClaudeState{},
			ArbiterNote: "still fresh"},
		// non-hook but still needs-input (bell title, derived status):
		// the episode isn't over yet
		{Session: "gamma", ID: "%3", Status: StatusNeedsInput, Title: "🔔 pick one",
			ArbiterNote: "asking something"},
		// coop's own sessions are never targets
		{Session: "roost", ID: "%0", Hub: true, ArbiterNote: "n/a"},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterNote: "n/a"},
	}
	f := &fakeTmux{panes: panes}
	for _, p := range panes {
		if p.ArbiterNudgedMark {
			f.SetPaneOption(p.ID, ArbiterNudgedMarker, "1")
		}
		if p.ArbiterNote != "" {
			f.SetPaneOption(p.ID, ArbiterNoteMarker, p.ArbiterNote)
		}
		if p.ArbiterSuggest != "" {
			f.SetPaneOption(p.ID, ArbiterSuggestMarker, p.ArbiterSuggest)
		}
	}
	RetireStaleEpisodes(f, panes)
	for _, m := range []string{ArbiterNudgedMarker, ArbiterNoteMarker, ArbiterSuggestMarker} {
		if _, ok := f.paneOpts["%1/"+m]; ok {
			t.Errorf("alpha's %s survived retirement", m)
		}
	}
	if f.paneOpts["%2/"+ArbiterNoteMarker] != "still fresh" {
		t.Error("hook pane's note was cleared by the non-hook path")
	}
	if f.paneOpts["%3/"+ArbiterNoteMarker] != "asking something" {
		t.Error("still-waiting pane's note was cleared")
	}
	if f.paneOpts["%0/"+ArbiterNoteMarker] != "n/a" {
		t.Error("hub pane's note was touched")
	}
	if f.paneOpts["%9/"+ArbiterNoteMarker] != "n/a" {
		t.Error("arbiter pane's own note was touched")
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
	out, err := Peek(f, &Transcripts{}, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Do it?") || strings.Contains(out, "\x1b[") {
		t.Errorf("Peek = %q, want stripped screen", out)
	}
	// arbiterPanes' alpha carries no Claude state, so there is nothing to
	// join a transcript through — the p.Claude branch must stay untaken.
	if strings.Contains(out, "last assistant message") {
		t.Error("Peek added a transcript section with no Claude state to key it")
	}
	if _, err := Peek(f, &Transcripts{}, "ghost"); err == nil {
		t.Error("peeked a missing session")
	}
}

// A pane whose session published Claude state (the hook path) gets its
// last transcript turn appended after the screen — the branch TestPeek
// above can't reach because arbiterPanes' pane has no p.Claude.
func TestPeekWithClaudeState(t *testing.T) {
	tr := writeTranscript(t, "/home/user/sprocket-v2", "sess-1",
		`{"type":"assistant","message":{"model":"claude-sonnet-5","content":[{"type":"text","text":"May I run the migration?"}]}}`)
	panes := []Pane{
		{Session: "alpha", ID: "%1",
			Claude: &ClaudeState{SessionID: "sess-1", CWD: "/home/user/sprocket-v2"}},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full"},
	}
	f := &fakeTmux{panes: panes, screen: "screen text\n"}
	out, err := Peek(f, tr, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "screen text") {
		t.Errorf("Peek = %q, want the screen section", out)
	}
	if !strings.Contains(out, "=== last assistant message ===\nMay I run the migration?") {
		t.Errorf("Peek = %q, want the transcript's last text turn", out)
	}
}

// Claude state with no matching transcript file (not flushed yet, or the
// session predates the hooks) must still return the screen — LastText's
// false is swallowed silently, never surfaced as an error.
func TestPeekClaudeStateNoTranscript(t *testing.T) {
	panes := []Pane{
		{Session: "alpha", ID: "%1",
			Claude: &ClaudeState{SessionID: "missing", CWD: "/home/user/sprocket-v2"}},
	}
	f := &fakeTmux{panes: panes, screen: "screen text\n"}
	out, err := Peek(f, &Transcripts{Dir: t.TempDir()}, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "screen text") {
		t.Errorf("Peek = %q, want the screen section", out)
	}
	if strings.Contains(out, "last assistant message") {
		t.Errorf("Peek = %q, want no transcript section for a missing file", out)
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
