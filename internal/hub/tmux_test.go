package hub

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// Real output shape: fields joined by \x1f (unit separator — pane titles
// can contain spaces and even tabs, but never \x1f). The last four
// fields are user options (@coop, @coop_working,
// @coop_done_since, @coop_notified), empty when unset.
func TestParsePanes(t *testing.T) {
	out := "roost\x1f%0\x1f795186\x1f✻ coop\x1f0\x1fcoop\x1f1753279140\x1f/home/user/coop\x1f1\x1f\x1f\x1f\n" +
		"sprocket-v2\x1f%8\x1f920957\x1f⠂ Simplify tmux session management\x1f1\x1fclaude\x1f1753286474\x1f/home/user/sprocket-v2\x1f\x1f1\x1f\x1f1\n"
	panes := parsePanes(out)
	if len(panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(panes))
	}
	want := Pane{
		Session: "sprocket-v2", ID: "%8", PID: 920957,
		Title: "⠂ Simplify tmux session management",
		Bell:  true, Cmd: "claude",
		Path:         "/home/user/sprocket-v2",
		Created:      time.Unix(1753286474, 0),
		WorkingMark:  true,
		NotifiedMark: true,
	}
	if panes[1] != want {
		t.Fatalf("got %+v\nwant %+v", panes[1], want)
	}
	if panes[0].Bell {
		t.Fatal("pane 0 should not have bell set")
	}
	if !panes[0].Hub || panes[1].Hub {
		t.Fatalf("only pane 0 is a hub: got %v, %v", panes[0].Hub, panes[1].Hub)
	}
}

func TestParsePanesDoneSince(t *testing.T) {
	panes := parsePanes("s\x1f%1\x1f4242\x1ftitle\x1f0\x1fclaude\x1f100\x1f/tmp/s\x1f\x1f\x1f1753286000\x1f\n")
	if len(panes) != 1 || !panes[0].DoneSince.Equal(time.Unix(1753286000, 0)) {
		t.Fatalf("done-since should parse as unix time, got %+v", panes)
	}
	// Unset and garbage both read as zero — badge simply not armed.
	for _, raw := range []string{"", "notanumber"} {
		panes = parsePanes("s\x1f%1\x1f4242\x1ftitle\x1f0\x1fclaude\x1f100\x1f/tmp/s\x1f\x1f\x1f" + raw + "\x1f\n")
		if len(panes) != 1 || !panes[0].DoneSince.IsZero() {
			t.Fatalf("done-since %q should yield zero time, got %+v", raw, panes)
		}
	}
}

func TestParsePanesSkipsMalformed(t *testing.T) {
	out := "garbage line with no separators\n" +
		"ok\x1f%1\x1f4242\x1ftitle\x1f0\x1fclaude\x1f100\x1f/tmp/ok\x1f\x1f\x1f\x1f\n" +
		"\n"
	panes := parsePanes(out)
	if len(panes) != 1 || panes[0].Session != "ok" {
		t.Fatalf("got %+v, want just the ok pane", panes)
	}
}

func TestParsePanesBadTimestamp(t *testing.T) {
	panes := parsePanes("s\x1f%1\x1f4242\x1ftitle\x1f0\x1fclaude\x1fnotanumber\x1f/tmp/s\x1f\x1f\x1f\x1f\n")
	if len(panes) != 1 || !panes[0].Created.IsZero() {
		t.Fatalf("bad timestamp should yield zero time, got %+v", panes)
	}
}

// An unreadable pid reads as 0 — no session file lookup, so the pane
// falls back to title-derived status.
func TestParsePanesBadPID(t *testing.T) {
	panes := parsePanes("s\x1f%1\x1f\x1ftitle\x1f0\x1fclaude\x1f100\x1f/tmp/s\x1f\x1f\x1f\x1f\n")
	if len(panes) != 1 || panes[0].PID != 0 {
		t.Fatalf("bad pid should yield 0, got %+v", panes)
	}
}

func TestParseSessions(t *testing.T) {
	out := "roost\x1f1\x1f1\n" +
		"roost-2\x1f0\x1f1\n" +
		"sprocket-v2\x1f0\x1f\n" +
		"garbage\n" +
		"\n"
	got := parseSessions(out)
	want := []SessionInfo{
		{Name: "roost", Attached: true, Hub: true},
		{Name: "roost-2", Attached: false, Hub: true},
		{Name: "sprocket-v2", Attached: false, Hub: false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// Integration: ResizeWindow's exact tmux argv against a real server —
// the "=session" vs "=session:" target distinction is invisible to fakes
// and set-option -w rejects the former with "no such window".
func TestExecResizeWindowAgainstRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := &ExecTmux{Socket: fmt.Sprintf("coop-test-%d", os.Getpid())}
	if _, err := tm.run("new-session", "-d", "-s", "coop-3",
		"-x", "80", "-y", "24", "sleep 60"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	t.Cleanup(func() { tm.run("kill-server") })

	if err := tm.ResizeWindow("coop-3", 100, 30); err != nil {
		t.Fatalf("ResizeWindow: %v", err)
	}
	size, err := tm.run("display-message", "-p", "-t", "coop-3",
		"#{window_width}x#{window_height}")
	if err != nil {
		t.Fatalf("display-message: %v", err)
	}
	if got := strings.TrimSpace(size); got != "100x30" {
		t.Errorf("window size = %q, want 100x30", got)
	}
	// resize-window flips window-size to manual; ResizeWindow must have
	// restored latest or a later primary client attach won't resize.
	opt, err := tm.run("show-options", "-w", "-t", "coop-3:", "window-size")
	if err != nil {
		t.Fatalf("show-options: %v", err)
	}
	if got := strings.TrimSpace(opt); got != "window-size latest" {
		t.Errorf("window-size option = %q, want %q", got, "window-size latest")
	}
}

func TestIsNoServer(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"tmux list-panes: exit status 1: no server running on /tmp/tmux-1000/cc", true},
		{"tmux list-panes: exit status 1: error connecting to /tmp/tmux-1000/cc (No such file or directory)", true},
		{"tmux list-panes: exit status 1: unknown option", false},
	}
	for _, c := range cases {
		if got := isNoServer(errors.New(c.msg)); got != c.want {
			t.Errorf("isNoServer(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
	if isNoServer(nil) {
		t.Error("isNoServer(nil) should be false")
	}
}
