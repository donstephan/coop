package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAudit(t *testing.T) {
	// Path with a missing parent dir — AppendAudit must create it.
	path := filepath.Join(t.TempDir(), "state", "audit.jsonl")
	e1 := AuditEntry{Time: time.Unix(1700000000, 0).UTC(), Session: "alpha",
		Action: "answered", Digit: "1", Reason: "running tests", Dialog: "Run go test?"}
	e2 := AuditEntry{Time: time.Unix(1700000060, 0).UTC(), Session: "beta",
		Action: "escalated", Reason: "asking about schema design"}
	if err := AppendAudit(path, e1); err != nil {
		t.Fatal(err)
	}
	if err := AppendAudit(path, e2); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var got AuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Session != "alpha" || got.Action != "answered" || got.Digit != "1" {
		t.Errorf("line 1 = %+v", got)
	}
	// escalated entries carry no digit key at all.
	if strings.Contains(lines[1], `"digit"`) {
		t.Errorf("escalated entry has digit key: %s", lines[1])
	}
}

func TestAppendAuditEmptyPath(t *testing.T) {
	if err := AppendAudit("", AuditEntry{}); err == nil {
		t.Error("empty path did not error")
	}
}

func TestDefaultAuditPathXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/home/user/.local/state")
	want := filepath.Join("/home/user/.local/state", "coop", "arbiter-audit.jsonl")
	if got := DefaultAuditPath(); got != want {
		t.Errorf("DefaultAuditPath = %q, want %q", got, want)
	}
}
