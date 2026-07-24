package main

import (
	"os"
	"slices"
	"testing"

	"coop/internal/hub"
)

// indexOfSubslice returns the index where needle first appears as a
// contiguous run inside haystack, or -1.
func indexOfSubslice(haystack, needle []string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

func TestCreateArgvChainsDefaults(t *testing.T) {
	got := createArgv("/bin/self", "cc", "claude,node", "/cfg.json", "claude",
		"5m", nil, "roost-2")

	// -f /dev/null keeps the user's personal tmux.conf off the hub server.
	prefix := []string{"tmux", "-L", "cc", "-f", os.DevNull, "start-server"}
	if indexOfSubslice(got, prefix) != 0 {
		t.Fatalf("argv = %v, want prefix %v", got, prefix)
	}

	suffix := []string{";", "new-session", "-d", "-s", "roost-2",
		"/bin/self", "-socket", "cc", "-allowed-cmds", "claude,node",
		"-config", "/cfg.json", "-claude-cmd", "claude", "-done-ttl", "5m",
		";", "set-option", "-t", "roost-2:", "@coop", "1"}
	if i := indexOfSubslice(got, suffix); i == -1 || i+len(suffix) != len(got) {
		t.Fatalf("argv = %v, want suffix %v", got, suffix)
	}

	// The hub-critical settings must ride in the chain before new-session.
	for _, cmd := range [][]string{
		{";", "set", "-wg", "monitor-bell", "on"},
		{";", "set", "-g", "status", "off"},
		{";", "set", "-g", "default-terminal", "tmux-256color"},
	} {
		if indexOfSubslice(got, cmd) == -1 {
			t.Fatalf("argv = %v, missing chained command %v", got, cmd)
		}
	}
}

func TestCreateArgvAppendsOverridesLast(t *testing.T) {
	got := createArgv("/bin/self", "cc", "claude,node", "/cfg.json", "claude",
		"5m", [][]string{{"set", "-g", "mouse", "off"}}, "roost")

	// Overrides come after every default (so they win), right before
	// new-session.
	tail := []string{";", "set", "-g", "mouse", "off", ";", "new-session"}
	if indexOfSubslice(got, tail) == -1 {
		t.Fatalf("argv = %v, want override right before new-session: %v", got, tail)
	}
	if i := indexOfSubslice(got, []string{";", "set", "-g", "mouse", "on"}); i == -1 {
		t.Fatalf("argv = %v, default mouse setting missing", got)
	} else if i > indexOfSubslice(got, tail) {
		t.Fatalf("argv = %v, default appears after override", got)
	}
}

func TestAttachArgv(t *testing.T) {
	got := attachArgv("cc", "roost-2")
	want := []string{"tmux", "-L", "cc", "attach-session", "-t", "=roost-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

// A hub session nobody is attached to is an orphan (its terminal
// closed) — relaunching adopts it instead of stacking up new hubs.
func TestPickDetachedHub(t *testing.T) {
	sessions := []hub.SessionInfo{
		{Name: "roost", Attached: true, Hub: true},
		{Name: "alpha", Attached: false, Hub: false},
		{Name: "roost-2", Attached: false, Hub: true},
	}
	if got := pickDetachedHub(sessions); got != "roost-2" {
		t.Fatalf("pickDetachedHub = %q, want roost-2", got)
	}
	if got := pickDetachedHub(sessions[:2]); got != "" {
		t.Fatalf("no detached hub should pick nothing, got %q", got)
	}
	if got := pickDetachedHub(nil); got != "" {
		t.Fatalf("no sessions should pick nothing, got %q", got)
	}
}

// The new hub's name must dodge every existing session, hub or not —
// a repo session named "roost-2" blocks that name just as another hub
// instance does.
func TestNextHubName(t *testing.T) {
	cases := []struct {
		names []string
		want  string
	}{
		{nil, "roost"},
		{[]string{"roost"}, "roost-2"},
		{[]string{"roost", "roost-2", "alpha"}, "roost-3"},
		{[]string{"roost", "roost-2"}, "roost-3"},
		{[]string{"alpha"}, "roost"},
	}
	for _, c := range cases {
		var sessions []hub.SessionInfo
		for _, n := range c.names {
			sessions = append(sessions, hub.SessionInfo{Name: n, Hub: n == "roost"})
		}
		if got := nextHubName(sessions); got != c.want {
			t.Errorf("nextHubName(%v) = %q, want %q", c.names, got, c.want)
		}
	}
}
