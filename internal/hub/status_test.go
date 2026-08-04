package hub

import (
	"strings"
	"testing"
)

// Fixture titles observed from live Claude Code sessions on 2026-07-23.
func TestStatusFor(t *testing.T) {
	cases := []struct {
		name string
		pane Pane
		want Status
	}{
		{"bell flag wins", Pane{Title: "✻ sprocket-v2", Bell: true}, StatusNeedsInput},
		{"bell emoji in title", Pane{Title: "🔔 needs your input"}, StatusNeedsInput},
		{"braille spinner = working", Pane{Title: "⠂ Simplify tmux session management"}, StatusWorking},
		{"other spinner frame", Pane{Title: "⠧ thinking"}, StatusWorking},
		{"default title = idle", Pane{Title: "✳ Claude Code"}, StatusIdle},
		{"named idle", Pane{Title: "✻ sprocket-v2"}, StatusIdle},
		{"empty title = idle", Pane{Title: ""}, StatusIdle},
		{"bell emoji beats spinner", Pane{Title: "⠂ working 🔔"}, StatusNeedsInput},
	}
	for _, c := range cases {
		if got := StatusFor(c.pane); got != c.want {
			t.Errorf("%s: StatusFor(%q, bell=%v) = %v, want %v",
				c.name, c.pane.Title, c.pane.Bell, got, c.want)
		}
	}
}

// The injected hooks publish a status onto the pane (see hook.go),
// which beats every heuristic StatusFor otherwise applies to the title —
// except the title cross-checks covered by TestStatusForHookCrossChecks
// (busy + bell, waiting + spinner), which prove the published state stale.
func TestStatusForPrefersClaudeState(t *testing.T) {
	cases := []struct {
		name string
		pane Pane
		want Status
	}{
		{"busy beats plain title",
			Pane{Title: "✻ coop", Claude: &ClaudeState{Status: "busy"}}, StatusWorking},
		{"idle beats spinner title",
			Pane{Title: "⠂ working", Claude: &ClaudeState{Status: "idle"}}, StatusIdle},
		{"idle beats a latched bell",
			Pane{Title: "✻ coop", Bell: true, Claude: &ClaudeState{Status: "idle"}}, StatusIdle},
		{"waiting without any bell",
			Pane{Title: "✻ coop", Claude: &ClaudeState{Status: "waiting"}}, StatusNeedsInput},
		{"unrecognized status falls back to the title",
			Pane{Title: "⠂ working", Claude: &ClaudeState{Status: "hibernating"}}, StatusWorking},
	}
	for _, c := range cases {
		if got := StatusFor(c.pane); got != c.want {
			t.Errorf("%s: StatusFor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStatusForHookCrossChecks(t *testing.T) {
	cases := []struct {
		name string
		pane Pane
		want Status
	}{
		{"hook wins over stale bell", Pane{Bell: true,
			Claude: &ClaudeState{Status: "idle"}}, StatusIdle},
		{"waiting + spinner = approved long tool", Pane{Title: "⠇ npm test",
			Claude: &ClaudeState{Status: "waiting"}}, StatusWorking},
		{"busy + bell = rang since", Pane{Bell: true,
			Claude: &ClaudeState{Status: "busy"}}, StatusNeedsInput},
		{"busy + bell emoji", Pane{Title: "🔔 pick one",
			Claude: &ClaudeState{Status: "busy"}}, StatusNeedsInput},
		{"waiting, no spinner", Pane{Title: "✳ Claude Code",
			Claude: &ClaudeState{Status: "waiting"}}, StatusNeedsInput},
		{"unrecognized status falls to title", Pane{Title: "⠇ working",
			Claude: &ClaudeState{Status: "pondering"}}, StatusWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusFor(tc.pane); got != tc.want {
				t.Errorf("StatusFor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStatusString(t *testing.T) {
	if StatusNeedsInput.String() != "NEEDS INPUT" ||
		StatusWorking.String() != "working" ||
		StatusIdle.String() != "idle" {
		t.Fatal("Status.String() labels wrong")
	}
}

// Panes from two interleaved repos: groups become contiguous and
// alphabetical, rows inside each group keep tmux's own order — status
// never moves anything.
func TestSortPanesGroupsByRepo(t *testing.T) {
	panes := []Pane{
		{Session: "alpha", Path: "/r/alpha", Title: "✳ Claude Code"},
		{Session: "beta", Path: "/r/beta", Title: "✳ Claude Code"},
		{Session: "alpha-2", Path: "/r/alpha", Title: "⠂ compiling"},
		{Session: "beta-2", Path: "/r/beta", Bell: true, Title: "✻ b2"},
	}
	DeriveStatuses(panes)
	SortPanes(panes)
	want := []string{"alpha", "alpha-2", "beta", "beta-2"}
	for i := range want {
		if panes[i].Session != want[i] {
			t.Fatalf("order %v, want %v", sessions(panes), want)
		}
	}
}

// A repo group containing a needs-input pane stays put — the status
// column and the tab jump carry the signal, not the sort.
func TestSortPanesNeedsInputDoesNotHoist(t *testing.T) {
	panes := []Pane{
		{Session: "a1", Path: "/r/alpha", Title: "✳ Claude Code"},
		{Session: "m1", Path: "/r/mid", Title: "✳ Claude Code"},
		{Session: "z1", Path: "/r/zeta", Title: "🔔 pick an option"},
		{Session: "z2", Path: "/r/zeta", Title: "✳ Claude Code"},
	}
	DeriveStatuses(panes)
	SortPanes(panes)
	want := []string{"a1", "m1", "z1", "z2"}
	for i := range want {
		if panes[i].Session != want[i] {
			t.Fatalf("order %v, want %v", sessions(panes), want)
		}
	}
}

// Groups are alphabetical by repo name regardless of tmux order.
func TestSortPanesGroupsAlphabetical(t *testing.T) {
	panes := []Pane{
		{Session: "z", Path: "/r/zeta", Title: "✳ Claude Code"},
		{Session: "a", Path: "/r/alpha", Title: "✳ Claude Code"},
	}
	DeriveStatuses(panes)
	SortPanes(panes)
	if panes[0].Session != "a" || panes[1].Session != "z" {
		t.Fatalf("groups should sort alphabetically, got %v", sessions(panes))
	}
}

func sessions(panes []Pane) []string {
	var s []string
	for _, p := range panes {
		s = append(s, p.Session)
	}
	return s
}

func TestPaneRepo(t *testing.T) {
	if r := (Pane{Path: "/home/user/Documents/coop/git/coop"}).Repo(); r != "coop" {
		t.Errorf("Repo() = %q, want coop", r)
	}
	if r := (Pane{}).Repo(); r != "?" {
		t.Errorf("empty path Repo() = %q, want ?", r)
	}
}

// Within one group (all share the empty-path "?" repo) tmux order is
// preserved no matter what the statuses are.
func TestSortPanesStableWithinGroup(t *testing.T) {
	panes := []Pane{
		{Session: "a-idle", Title: "✳ Claude Code"},
		{Session: "b-work", Title: "⠂ compiling"},
		{Session: "c-bell", Bell: true, Title: "✻ c"},
		{Session: "d-idle", Title: "✻ d"},
		{Session: "e-bell", Title: "🔔 pick an option"},
	}
	DeriveStatuses(panes)
	SortPanes(panes)
	want := []string{"a-idle", "b-work", "c-bell", "d-idle", "e-bell"}
	for i := range want {
		if panes[i].Session != want[i] {
			t.Fatalf("order %v, want %v", sessions(panes), want)
		}
	}
}

// Screen fixtures captured from live Claude Code panes (2026-07-23).
const screenQuestionDialog = ` ☐ Debug probe 2
Second probe — answer anything; the screen recorder is capturing the text.
❯ 1. Done
     Captures the dialog footer text as a real fixture.
  2. Fine
     Same effect.
  3. Type something.
Enter to select · ↑/↓ to navigate · Esc to cancel`

const screenPermissionDialog = `Bash(rm -rf build/)
Do you want to proceed?
❯ 1. Yes
  2. No, and tell Claude what to do differently (esc)`

const screenIdle = `▐▛███▜▌   Claude Code v2.1.218
▝▜█████▛▘  Fable 5 · Claude Max
❯ 
  ⏸ manual mode on · ? for shortcuts · ← for agents`

const screenWorking = `● Confirmed — the Vitest layer parses real PEMs cleanly.
+ Choreographing… (2m 43s · ↓ 8.6k tokens · thinking)
  Tip: Use /btw to ask a quick side question`

func TestNeedsInputScreen(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"question dialog", screenQuestionDialog, true},
		{"permission dialog", screenPermissionDialog, true},
		{"idle prompt", screenIdle, false},
		{"working", screenWorking, false},
		{"empty", "", false},
		{"bare caret is not an option row", "❯ \n", false},
		{"do-you-want prose alone is not a dialog", "Do you want me to refactor this?\n❯ \n", false},
	}
	for _, c := range cases {
		if got := NeedsInputScreen(c.screen); got != c.want {
			t.Errorf("%s: NeedsInputScreen = %v, want %v", c.name, got, c.want)
		}
	}
}

// Dialog markers must only count near the bottom of the screen — an old
// answered dialog high up in a tall capture is history, not a prompt.
func TestNeedsInputScreenIgnoresScrollback(t *testing.T) {
	old := screenQuestionDialog + "\n" + strings.Repeat("filler line\n", 30) + "❯ \n"
	if NeedsInputScreen(old) {
		t.Error("dialog text outside the last 15 lines should not count")
	}
}

// Panes with no hook-published Claude state (nothing in p.Claude) fall
// back to the title heuristics entirely — this is that fallback's only
// input, so DeriveStatuses must still produce a status from it alone.
func TestDeriveStatusesTitleFallback(t *testing.T) {
	panes := []Pane{{ID: "%1", Title: "✳ x"}, {ID: "%2", Title: "⠂ y"}}
	DeriveStatuses(panes)
	if panes[0].Status != StatusIdle || panes[1].Status != StatusWorking {
		t.Fatalf("title-only panes should still derive a status, got %v %v",
			panes[0].Status, panes[1].Status)
	}
}

// Real capture-pane -e output wraps everything in color codes and uses
// NBSP after the caret — the matcher must see through both.
func TestNeedsInputScreenWithANSICapture(t *testing.T) {
	ansi := "\x1b[1mDo you want to proceed?\x1b[0m\n" +
		"\x1b[38;5;246m❯ \x1b[39m1. Yes\n" +
		"  \x1b[38;5;246m2. No, and tell Claude what to do differently (esc)\x1b[39m\n"
	if !NeedsInputScreen(ansi) {
		t.Error("ANSI-wrapped dialog should be detected")
	}
	ansiIdle := "\x1b[38;5;246m❯ \x1b[39m\n\x1b[2m⏸ manual mode on · ? for shortcuts\x1b[0m\n"
	if NeedsInputScreen(ansiIdle) {
		t.Error("ANSI-wrapped idle caret must not be detected")
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[1mbold\x1b[0m and \x1b]0;title\x07plain"
	if got := StripANSI(in); got != "bold and plain" {
		t.Errorf("StripANSI = %q", got)
	}
}
