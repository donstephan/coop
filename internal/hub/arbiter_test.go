package hub

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArbiterModeReadsGlobalOption(t *testing.T) {
	for _, tc := range []struct{ set, want string }{
		{"", ArbiterModeOff},
		{"off", ArbiterModeOff},
		{"recommend", ArbiterModeRecommend},
		// The mode an older coop wrote for what used to answer dialogs.
		// It must degrade to off, not to recommend: an unrecognized mode
		// has always meant "do nothing", and silently promoting it to the
		// judging mode would resurrect the setting this change removes.
		{"full", ArbiterModeOff},
		{"gremlin", ArbiterModeOff}, // a later coop's value never enables judging
	} {
		f := &fakeTmux{globals: map[string]string{ArbiterModeMarker: tc.set}}
		if got := ArbiterMode(f); got != tc.want {
			t.Errorf("ArbiterMode(%q) = %q, want %q", tc.set, got, tc.want)
		}
	}
}

func TestSetArbiterModeOffUnsets(t *testing.T) {
	f := &fakeTmux{globals: map[string]string{ArbiterModeMarker: "recommend"}}
	if err := SetArbiterMode(f, ArbiterModeOff); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.globals[ArbiterModeMarker]; ok {
		t.Error("off should unset the option, not write a value")
	}
	if err := SetArbiterMode(f, ArbiterModeRecommend); err != nil {
		t.Fatal(err)
	}
	if got := f.globals[ArbiterModeMarker]; got != ArbiterModeRecommend {
		t.Errorf("got %q, want %q", got, ArbiterModeRecommend)
	}
}

// A value SetArbiterMode has never written on purpose — the "full" an
// older coop wrote is now just one instance of it — still has to unset
// the marker: anything that isn't recommend means off, and off leaves no
// option behind.
func TestSetArbiterModeUnrecognizedValueUnsets(t *testing.T) {
	f := &fakeTmux{globals: map[string]string{ArbiterModeMarker: "recommend"}}
	if err := SetArbiterMode(f, "full"); err != nil {
		t.Fatalf("SetArbiterMode: %v", err)
	}
	if v, ok := f.globals[ArbiterModeMarker]; ok {
		t.Fatalf("marker still set to %q, want unset", v)
	}
}

func TestArbiterModeErrorReadsOff(t *testing.T) {
	f := &fakeTmux{err: errors.New("no server")}
	if got := ArbiterMode(f); got != ArbiterModeOff {
		t.Errorf("got %q, want off on error", got)
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
			ArbiterNote: "old note", ArbiterSuggest: "2"},
		// hook pane (Claude set): ApplyHook owns its cleanup, untouched here
		{Session: "beta", ID: "%2", Status: StatusIdle, Claude: &ClaudeState{},
			ArbiterNote: "still fresh"},
		// non-hook but still needs-input (bell title, derived status):
		// the episode isn't over yet
		{Session: "gamma", ID: "%3", Status: StatusNeedsInput, Title: "🔔 pick one",
			ArbiterNote: "asking something"},
		// coop's own hub session is never a target
		{Session: "roost", ID: "%0", Hub: true, ArbiterNote: "n/a"},
	}
	f := &fakeTmux{panes: panes}
	for _, p := range panes {
		if p.ArbiterNote != "" {
			f.SetPaneOption(p.ID, ArbiterNoteMarker, p.ArbiterNote)
		}
		if p.ArbiterSuggest != "" {
			f.SetPaneOption(p.ID, ArbiterSuggestMarker, p.ArbiterSuggest)
		}
	}
	RetireStaleEpisodes(f, panes)
	for _, m := range []string{ArbiterNoteMarker, ArbiterSuggestMarker} {
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

// arbiterPanes is a socket with one target session — the baseline every
// gate test perturbs. The arbiter mode itself is a global tmux option
// now (see fakeTmux.globals), not carried by any pane.
func arbiterPanes() []Pane {
	return []Pane{
		{Session: "alpha", ID: "%1", PID: 0},
	}
}

func TestNote(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	f := &fakeTmux{panes: arbiterPanes()}
	warn, err := Note(f, NoteReq{PaneID: "%1", Text: "asking to drop a table\n— suggest 2",
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
	hub := &fakeTmux{panes: []Pane{{Session: "roost", ID: "%0", Hub: true}}}
	if _, err := Note(hub, NoteReq{PaneID: "%0", Text: "x", Audit: audit}); err == nil {
		t.Error("noted coop's own hub session")
	}
}

// The suggestion is its own option, and an unsuggested note clears any
// digit an earlier one left — the row must never offer a stale answer
// under fresh text.
func TestNoteSuggest(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	f := &fakeTmux{panes: arbiterPanes()}
	if _, err := Note(f, NoteReq{PaneID: "%1", Text: "asking to run tests",
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
	if _, err := Note(f, NoteReq{PaneID: "%1", Text: "now asking something else",
		Audit: audit, Now: time.Unix(1700000200, 0)}); err != nil {
		t.Fatalf("Note = %v", err)
	}
	if _, ok := f.paneOpts["%1/"+ArbiterSuggestMarker]; ok {
		t.Error("suggestion survived a note that named none")
	}

	// A bad digit is refused before anything is written.
	f = &fakeTmux{panes: arbiterPanes()}
	if _, err := Note(f, NoteReq{PaneID: "%1", Text: "t", Suggest: "12",
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

// ArbiterHome's own behavior (seeding arbiter.md, no settings.json) is
// covered by TestArbiterHomeSeedsPolicyAndNoSettings and
// TestArbiterHomeLeavesExistingPolicyAlone in judgeexec_test.go.
