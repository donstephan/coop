package hub

import (
	"testing"
	"time"
)

// applyRound derives base statuses and runs the tracker over one pane
// per title, returning the resulting statuses keyed by pane id. The
// fake's pane-option store stands in for the tmux server: WorkingMark
// and DoneSince are read back from it each round, so state written by
// one tracker is visible to any tracker sharing the fake — exactly the
// cross-hub protocol.
func applyRound(t *DoneTracker, f *fakeTmux, titles map[string]string,
	visited func(string) bool, now time.Time) map[string]Status {
	var panes []Pane
	for id, title := range titles {
		panes = append(panes, Pane{
			ID: id, Session: "s-" + id, Title: title,
			WorkingMark: f.paneOpts[id+"/"+WorkingMarker] == "1",
			DoneSince:   unixTime(f.paneOpts[id+"/"+DoneSinceMarker]),
		})
	}
	DeriveStatuses(panes, nil)
	t.Apply(panes, visited, now)
	got := map[string]Status{}
	for _, p := range panes {
		got[p.ID] = p.Status
	}
	return got
}

const (
	titleIdle    = "✳ Claude Code"
	titleWorking = "⠂ compiling"
	titleBell    = "🔔 pick an option"
)

var t0 = time.Unix(1000, 0)

func TestDoneArmsOnWorkingToIdle(t *testing.T) {
	f := &fakeTmux{}
	tr := NewDoneTracker(5*time.Minute, f)
	applyRound(tr, f, map[string]string{"%1": titleWorking}, nil, t0)
	got := applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(time.Second))
	if got["%1"] != StatusDone {
		t.Fatalf("working→idle should show done, got %v", got["%1"])
	}
	// Still done just under the TTL, measured from when it finished.
	got = applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(5*time.Minute))
	if got["%1"] != StatusDone {
		t.Fatalf("done should persist under the TTL, got %v", got["%1"])
	}
}

func TestDoneDecaysAfterTTL(t *testing.T) {
	f := &fakeTmux{}
	tr := NewDoneTracker(5*time.Minute, f)
	applyRound(tr, f, map[string]string{"%1": titleWorking}, nil, t0)
	applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(time.Second))
	got := applyRound(tr, f, map[string]string{"%1": titleIdle}, nil,
		t0.Add(time.Second+5*time.Minute))
	if got["%1"] != StatusIdle {
		t.Fatalf("done should decay to idle after the TTL, got %v", got["%1"])
	}
	// Decay is permanent — a later poll inside no window revives it.
	got = applyRound(tr, f, map[string]string{"%1": titleIdle}, nil,
		t0.Add(2*time.Second+5*time.Minute))
	if got["%1"] != StatusIdle {
		t.Fatalf("decayed pane must stay idle, got %v", got["%1"])
	}
}

func TestDoneClearsOnVisit(t *testing.T) {
	f := &fakeTmux{}
	tr := NewDoneTracker(5*time.Minute, f)
	applyRound(tr, f, map[string]string{"%1": titleWorking}, nil, t0)
	visited := func(session string) bool { return session == "s-%1" }
	got := applyRound(tr, f, map[string]string{"%1": titleIdle}, visited, t0.Add(time.Second))
	if got["%1"] != StatusIdle {
		t.Fatalf("a visited session must not show done, got %v", got["%1"])
	}
	// The clear sticks after the visit ends.
	got = applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(2*time.Second))
	if got["%1"] != StatusIdle {
		t.Fatalf("done must stay cleared after a visit, got %v", got["%1"])
	}
}

func TestDoneClearsWhenWorkingResumes(t *testing.T) {
	f := &fakeTmux{}
	tr := NewDoneTracker(5*time.Minute, f)
	applyRound(tr, f, map[string]string{"%1": titleWorking}, nil, t0)
	applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(time.Second))
	got := applyRound(tr, f, map[string]string{"%1": titleWorking}, nil, t0.Add(2*time.Second))
	if got["%1"] != StatusWorking {
		t.Fatalf("resumed work should show working, got %v", got["%1"])
	}
	// Finishing again re-arms from the new finish time.
	got = applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(3*time.Second))
	if got["%1"] != StatusDone {
		t.Fatalf("second finish should re-arm done, got %v", got["%1"])
	}
}

func TestNeedsInputBeatsDone(t *testing.T) {
	f := &fakeTmux{}
	tr := NewDoneTracker(5*time.Minute, f)
	applyRound(tr, f, map[string]string{"%1": titleWorking}, nil, t0)
	applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(time.Second))
	got := applyRound(tr, f, map[string]string{"%1": titleBell}, nil, t0.Add(2*time.Second))
	if got["%1"] != StatusNeedsInput {
		t.Fatalf("needs-input must override done, got %v", got["%1"])
	}
	// The dialog also disarms: back to idle without working = idle.
	got = applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(3*time.Second))
	if got["%1"] != StatusIdle {
		t.Fatalf("needs-input should disarm done, got %v", got["%1"])
	}
}

func TestIdleWithoutWorkingStaysIdle(t *testing.T) {
	f := &fakeTmux{}
	tr := NewDoneTracker(5*time.Minute, f)
	got := applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0)
	if got["%1"] != StatusIdle {
		t.Fatalf("never-working pane must stay idle, got %v", got["%1"])
	}
}

func TestDoneDisabled(t *testing.T) {
	for _, tr := range []*DoneTracker{nil, NewDoneTracker(0, &fakeTmux{})} {
		f := &fakeTmux{}
		applyRound(tr, f, map[string]string{"%1": titleWorking}, nil, t0)
		got := applyRound(tr, f, map[string]string{"%1": titleIdle}, nil, t0.Add(time.Second))
		if got["%1"] != StatusIdle {
			t.Fatalf("disabled tracker must never set done, got %v", got["%1"])
		}
		if len(f.paneOpts) != 0 {
			t.Fatalf("disabled tracker must not write options, got %v", f.paneOpts)
		}
	}
}

// Two trackers sharing one tmux server (two hub instances): state
// written by either is seen by both, and a visit through one clears
// the badge for the other.
func TestDoneSharedAcrossTrackers(t *testing.T) {
	f := &fakeTmux{}
	a := NewDoneTracker(5*time.Minute, f)
	b := NewDoneTracker(5*time.Minute, f)
	pane := map[string]string{"%1": titleIdle}

	applyRound(a, f, map[string]string{"%1": titleWorking}, nil, t0)
	got := applyRound(b, f, pane, nil, t0.Add(time.Second))
	if got["%1"] != StatusDone {
		t.Fatalf("hub B should see the arm from hub A's working mark, got %v", got["%1"])
	}
	got = applyRound(a, f, pane, nil, t0.Add(2*time.Second))
	if got["%1"] != StatusDone {
		t.Fatalf("hub A should see done armed by hub B, got %v", got["%1"])
	}
	visited := func(session string) bool { return session == "s-%1" }
	applyRound(b, f, pane, visited, t0.Add(3*time.Second))
	got = applyRound(a, f, pane, nil, t0.Add(4*time.Second))
	if got["%1"] != StatusIdle {
		t.Fatalf("a visit via hub B must clear the badge for hub A, got %v", got["%1"])
	}
}

// Done is a status like any other under the stable sort: rows and
// groups stay where tmux order and repo name put them.
func TestSortPanesDoneDoesNotMoveRows(t *testing.T) {
	panes := []Pane{
		{Session: "w", Path: "/r/alpha", Title: titleWorking, Status: StatusWorking},
		{Session: "d", Path: "/r/alpha", Title: titleIdle, Status: StatusDone},
		{Session: "z1", Path: "/r/zeta", Title: titleBell, Status: StatusNeedsInput},
	}
	SortPanes(panes)
	want := []string{"w", "d", "z1"}
	for i := range want {
		if panes[i].Session != want[i] {
			t.Fatalf("order %v, want %v", sessions(panes), want)
		}
	}
}
