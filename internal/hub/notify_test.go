package hub

import (
	"slices"
	"testing"
)

// notifyRound derives base statuses and runs the tracker over one pane
// per title, returning the sessions Apply reported as newly needing a
// notification. As in done_test.go, the fake's pane-option store stands
// in for the tmux server: NotifiedMark is read back from it each round,
// so state written by one tracker is visible to any tracker sharing the
// fake — the cross-hub dedupe protocol.
func notifyRound(t *NotifyTracker, f *fakeTmux, titles map[string]string) []string {
	var panes []Pane
	for id, title := range titles {
		panes = append(panes, Pane{
			ID: id, Session: "s-" + id, Title: title,
			NotifiedMark: f.paneOpts[id+"/"+NotifiedMarker] == "1",
		})
	}
	DeriveStatuses(panes, nil)
	var got []string
	for _, p := range t.Apply(panes) {
		got = append(got, p.Session)
	}
	return got
}

func TestNotifyFiresOnNeedsInput(t *testing.T) {
	f := &fakeTmux{}
	tr := NewNotifyTracker(f)
	if got := notifyRound(tr, f, map[string]string{"%1": titleWorking}); got != nil {
		t.Fatalf("working pane must not notify, got %v", got)
	}
	got := notifyRound(tr, f, map[string]string{"%1": titleBell})
	if len(got) != 1 || got[0] != "s-%1" {
		t.Fatalf("entering needs-input should notify once, got %v", got)
	}
}

func TestNotifyOncePerEpisode(t *testing.T) {
	f := &fakeTmux{}
	tr := NewNotifyTracker(f)
	notifyRound(tr, f, map[string]string{"%1": titleBell})
	if got := notifyRound(tr, f, map[string]string{"%1": titleBell}); got != nil {
		t.Fatalf("still-waiting pane must not re-notify, got %v", got)
	}
}

func TestNotifyRearmsAfterEpisodeEnds(t *testing.T) {
	f := &fakeTmux{}
	tr := NewNotifyTracker(f)
	notifyRound(tr, f, map[string]string{"%1": titleBell})
	if got := notifyRound(tr, f, map[string]string{"%1": titleWorking}); got != nil {
		t.Fatalf("leaving needs-input must not notify, got %v", got)
	}
	got := notifyRound(tr, f, map[string]string{"%1": titleBell})
	if len(got) != 1 {
		t.Fatalf("a new needs-input episode should notify again, got %v", got)
	}
}

func TestNotifyIdleNeverFires(t *testing.T) {
	f := &fakeTmux{}
	tr := NewNotifyTracker(f)
	if got := notifyRound(tr, f, map[string]string{"%1": titleIdle}); got != nil {
		t.Fatalf("idle pane must not notify, got %v", got)
	}
	if len(f.paneOpts) != 0 {
		t.Fatalf("idle pane must not write options, got %v", f.paneOpts)
	}
}

// Two trackers sharing one tmux server (two hub instances): whichever
// hub polls first notifies; the marker it sets keeps the other quiet.
func TestNotifySharedAcrossTrackers(t *testing.T) {
	f := &fakeTmux{}
	a := NewNotifyTracker(f)
	b := NewNotifyTracker(f)
	if got := notifyRound(a, f, map[string]string{"%1": titleBell}); len(got) != 1 {
		t.Fatalf("hub A should notify first, got %v", got)
	}
	if got := notifyRound(b, f, map[string]string{"%1": titleBell}); got != nil {
		t.Fatalf("hub B must stay quiet after hub A notified, got %v", got)
	}
}

func TestNotifyNilTrackerIsNoOp(t *testing.T) {
	var tr *NotifyTracker
	if got := tr.Apply([]Pane{{ID: "%1", Status: StatusNeedsInput}}); got != nil {
		t.Fatalf("nil tracker must be a no-op, got %v", got)
	}
}

func TestNotifyArgsSessionAsTitleTaskAsBody(t *testing.T) {
	want := []string{"-a", "coop", "myrepo needs input", "Simplify tmux session…"}
	if got := notifyArgs("myrepo", "Simplify tmux session…"); !slices.Equal(got, want) {
		t.Fatalf("notifyArgs = %v, want %v", got, want)
	}
}

func TestNotifyArgsEmptyTaskOmitsBody(t *testing.T) {
	want := []string{"-a", "coop", "myrepo needs input"}
	if got := notifyArgs("myrepo", ""); !slices.Equal(got, want) {
		t.Fatalf("notifyArgs = %v, want %v", got, want)
	}
}
