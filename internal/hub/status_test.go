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
		{"bell flag wins", Pane{Title: "✻ dataset-v2", Bell: true}, StatusNeedsInput},
		{"bell emoji in title", Pane{Title: "🔔 needs your input"}, StatusNeedsInput},
		{"braille spinner = working", Pane{Title: "⠂ Simplify tmux session management"}, StatusWorking},
		{"other spinner frame", Pane{Title: "⠧ thinking"}, StatusWorking},
		{"default title = idle", Pane{Title: "✳ Claude Code"}, StatusIdle},
		{"named idle", Pane{Title: "✻ dataset-v2"}, StatusIdle},
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
	DeriveStatuses(panes, nil)
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
	DeriveStatuses(panes, nil)
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
	DeriveStatuses(panes, nil)
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
	DeriveStatuses(panes, nil)
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

func TestDeriveStatuses(t *testing.T) {
	panes := []Pane{
		{ID: "%1", Title: "✳ Claude Code"},       // idle, screen says dialog
		{ID: "%2", Title: "⠂ compiling"},         // working — screen not consulted
		{ID: "%3", Title: "✻ other", Bell: true}, // bell wins without capture
		{ID: "%4", Title: "✳ plain"},             // idle, screen idle
	}
	captured := map[string]string{"%1": screenPermissionDialog, "%4": screenIdle}
	var asked []string
	capture := func(pane string) (string, error) {
		asked = append(asked, pane)
		return captured[pane], nil
	}
	DeriveStatuses(panes, capture)
	want := []Status{StatusNeedsInput, StatusWorking, StatusNeedsInput, StatusIdle}
	for i, w := range want {
		if panes[i].Status != w {
			t.Errorf("pane %s: Status = %v, want %v", panes[i].ID, panes[i].Status, w)
		}
	}
	for _, id := range asked {
		if id == "%2" || id == "%3" {
			t.Errorf("captured %s, but non-idle panes must not be captured", id)
		}
	}
}

func TestDeriveStatusesNilCapture(t *testing.T) {
	panes := []Pane{{ID: "%1", Title: "✳ x"}, {ID: "%2", Title: "⠂ y"}}
	DeriveStatuses(panes, nil)
	if panes[0].Status != StatusIdle || panes[1].Status != StatusWorking {
		t.Fatalf("nil capture should still derive title statuses, got %v %v",
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
