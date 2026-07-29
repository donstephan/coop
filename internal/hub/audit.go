package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AuditEntry is one arbiter action, appended as a JSONL line. The audit
// file is the durable record (pane options are display state and die
// with their pane); coop never reads it back — it's for the operator.
type AuditEntry struct {
	Time    time.Time `json:"time"`
	Session string    `json:"session"`
	Action  string    `json:"action"` // "answered" | "escalated"
	Digit   string    `json:"digit,omitempty"`
	// Suggest is the digit an escalation offered for the human to apply;
	// kept apart from Digit so that field always means "a key was sent".
	Suggest string `json:"suggest,omitempty"`
	Reason  string `json:"reason"`
	Dialog  string `json:"dialog,omitempty"`
}

// DefaultAuditPath is $XDG_STATE_HOME/coop/arbiter-audit.jsonl, falling
// back to ~/.local/state. "" (no home) disables auditing gracefully —
// AppendAudit then errors and callers report it as a warning.
func DefaultAuditPath() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "coop", "arbiter-audit.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "coop", "arbiter-audit.jsonl")
}

// AppendAudit appends one entry, creating the directory on first use.
func AppendAudit(path string, e AuditEntry) error {
	if path == "" {
		return fmt.Errorf("no audit path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}
