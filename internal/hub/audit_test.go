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
		Action: "escalated", Suggest: "1", Reason: "running tests"}
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
	if got.Session != "alpha" || got.Action != "escalated" || got.Suggest != "1" {
		t.Errorf("line 1 = %+v", got)
	}
	// an escalation with no suggested digit carries no suggest key at all.
	if strings.Contains(lines[1], `"suggest"`) {
		t.Errorf("escalated entry has suggest key: %s", lines[1])
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

// DefaultJudgeLogPath shares stateDir's resolution with DefaultAuditPath
// — same XDG_STATE_HOME base, different filename alongside it.
func TestDefaultJudgeLogPathXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/home/user/.local/state")
	want := filepath.Join("/home/user/.local/state", "coop", "judge.log")
	if got := DefaultJudgeLogPath(); got != want {
		t.Errorf("DefaultJudgeLogPath = %q, want %q", got, want)
	}
}
