package hub

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Real shape of ~/.claude/sessions/<pid>.json (Claude Code 2.1.219),
// captured from a live session.
const sampleState = `{
  "pid": 920957,
  "sessionId": "f7737748-41b7-40d5-a5fd-02d383e35b66",
  "cwd": "/home/user/coop",
  "startedAt": 1784920994095,
  "procStart": "1936448",
  "version": "2.1.219",
  "peerProtocol": 1,
  "kind": "interactive",
  "entrypoint": "cli",
  "name": "coop-fd",
  "nameSource": "derived",
  "status": "busy",
  "updatedAt": 1784927027178,
  "statusUpdatedAt": 1784927027178
}`

// writeState drops a session file for pid and returns a reader whose
// PID-reuse guard agrees with whatever procStart the file claims.
func writeState(t *testing.T, pid int, body string) *ClaudeSessions {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return &ClaudeSessions{
		Dir:       dir,
		ProcStart: func(int) (string, bool) { return "1936448", true },
	}
}

func TestClaudeSessionsLookup(t *testing.T) {
	cs := writeState(t, 920957, sampleState)
	got := cs.Lookup(920957)
	if got == nil {
		t.Fatal("Lookup returned nil for a live session file")
	}
	want := ClaudeState{
		SessionID:   "f7737748-41b7-40d5-a5fd-02d383e35b66",
		Name:        "coop-fd",
		Status:      "busy",
		StatusSince: time.UnixMilli(1784927027178),
		Version:     "2.1.219",
		CWD:         "/home/user/coop",
	}
	if *got != want {
		t.Fatalf("got %+v\nwant %+v", *got, want)
	}
}

func TestClaudeSessionsLookupMissing(t *testing.T) {
	cs := writeState(t, 920957, sampleState)
	// No file for this pid — the pane isn't a Claude Code session, or
	// this is an older version that doesn't publish state.
	if got := cs.Lookup(4242); got != nil {
		t.Fatalf("missing session file should read as no state, got %+v", got)
	}
}

func TestClaudeSessionsLookupMalformed(t *testing.T) {
	cs := writeState(t, 1, "{not json")
	// Best-effort: a half-written file just means no state this poll.
	if got := cs.Lookup(1); got != nil {
		t.Fatalf("malformed file should read as no state, got %+v", got)
	}
}

func TestClaudeSessionsLookupRecycledPID(t *testing.T) {
	cs := writeState(t, 920957, sampleState)
	// A stale file left by a SIGKILLed Claude whose pid the kernel has
	// since handed to something else: procStart won't match.
	cs.ProcStart = func(int) (string, bool) { return "9999999", true }
	if got := cs.Lookup(920957); got != nil {
		t.Fatalf("recycled pid should read as no state, got %+v", got)
	}
}

func TestClaudeSessionsLookupUnverifiablePID(t *testing.T) {
	cs := writeState(t, 920957, sampleState)
	// Can't read the process start time (not Linux, permissions): trust
	// the file rather than lose the status entirely.
	cs.ProcStart = func(int) (string, bool) { return "", false }
	if got := cs.Lookup(920957); got == nil {
		t.Fatal("unverifiable pid should still yield state")
	}
}

func TestClaudeSessionsLookupNoTimestamp(t *testing.T) {
	cs := writeState(t, 1, `{"sessionId":"abc","status":"idle"}`)
	got := cs.Lookup(1)
	if got == nil {
		t.Fatal("a file without a timestamp should still yield state")
	}
	if !got.StatusSince.IsZero() {
		t.Fatalf("missing statusUpdatedAt should read as zero time, got %v", got.StatusSince)
	}
}

func TestClaudeStateStatus(t *testing.T) {
	cases := []struct {
		status string
		want   Status
		ok     bool
	}{
		{"busy", StatusWorking, true},
		{"waiting", StatusNeedsInput, true},
		{"idle", StatusIdle, true},
		// A status string a future version might add: no opinion, so
		// the caller falls back to reading the pane title.
		{"compacting", StatusIdle, false},
		{"", StatusIdle, false},
	}
	for _, c := range cases {
		got, ok := (&ClaudeState{Status: c.status}).status()
		if got != c.want || ok != c.ok {
			t.Errorf("status %q = (%v, %v), want (%v, %v)",
				c.status, got, ok, c.want, c.ok)
		}
	}
}

func TestPaneSince(t *testing.T) {
	created, entered := time.Unix(1000, 0), time.Unix(5000, 0)
	p := Pane{Created: created, Claude: &ClaudeState{Status: "idle", StatusSince: entered}}
	if got := p.Since(); !got.Equal(entered) {
		t.Errorf("published state should date the current status: got %v, want %v", got, entered)
	}
	// Nothing published, or published without a timestamp: the session's
	// own start is the only thing left to age from.
	for _, c := range []Pane{
		{Created: created},
		{Created: created, Claude: &ClaudeState{Status: "idle"}},
	} {
		if got := c.Since(); !got.Equal(created) {
			t.Errorf("%+v: got %v, want the session start %v", c.Claude, got, created)
		}
	}
}

func TestAttachClaudeState(t *testing.T) {
	panes := []Pane{{ID: "%1", PID: 100}, {ID: "%2", PID: 200}, {ID: "%3"}}
	lookup := func(pid int) *ClaudeState {
		if pid == 200 {
			return &ClaudeState{Status: "busy"}
		}
		return nil
	}
	AttachClaudeState(panes, lookup)
	if panes[0].Claude != nil || panes[2].Claude != nil {
		t.Fatal("panes without published state should keep a nil Claude")
	}
	if panes[1].Claude == nil || panes[1].Claude.Status != "busy" {
		t.Fatalf("pane %%2 should carry its state, got %+v", panes[1].Claude)
	}
}

func TestAttachClaudeStateSkipsPIDLess(t *testing.T) {
	called := 0
	AttachClaudeState([]Pane{{ID: "%1"}}, func(int) *ClaudeState {
		called++
		return nil
	})
	if called != 0 {
		t.Fatalf("pid 0 should not be looked up, got %d lookups", called)
	}
}
