package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClaudeState is the state Claude Code publishes for one of its
// processes in ~/.claude/sessions/<pid>.json. It is undocumented
// internal state (shape observed on 2.1.219), so every read here is
// best-effort: an absent, unreadable or unrecognized file just leaves
// the pane on the pane-title heuristics in StatusFor.
type ClaudeState struct {
	SessionID   string    // names the transcript, ~/.claude/projects/<slug>/<id>.jsonl
	Name        string    // Claude's own label for the session ("coop-fd")
	Status      string    // "busy" | "waiting" | "idle"
	StatusSince time.Time // when it entered Status (zero if unpublished)
	Version     string
	CWD         string
}

// claudeStateFile is the on-disk shape — only the fields we read.
type claudeStateFile struct {
	SessionID       string `json:"sessionId"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"` // unix milliseconds
	Version         string `json:"version"`
	CWD             string `json:"cwd"`
	ProcStart       string `json:"procStart"`
}

// status maps Claude's status onto ours. The bool is false for anything
// unrecognized — a status a later version added — so callers fall back
// to the title rather than mislabel a busy pane idle.
func (s *ClaudeState) status() (Status, bool) {
	switch s.Status {
	case "busy":
		return StatusWorking, true
	case "waiting":
		return StatusNeedsInput, true
	case "idle":
		return StatusIdle, true
	}
	return StatusIdle, false
}

// Since is when the pane entered its current status: Claude Code's own
// statusUpdatedAt where it published one, else the session's start time.
// "waiting 6m" is what makes a row worth switching to; "session started
// 3h ago" says nothing about whether it needs you.
func (p Pane) Since() time.Time {
	if p.Claude != nil && !p.Claude.StatusSince.IsZero() {
		return p.Claude.StatusSince
	}
	return p.Created
}

// claudeStatusKnown reports whether the pane's status can come from
// Claude Code itself rather than from its title.
func (p Pane) claudeStatusKnown() bool {
	if p.Claude == nil {
		return false
	}
	_, ok := p.Claude.status()
	return ok
}

// ClaudeSessions reads those per-process state files.
type ClaudeSessions struct {
	// Dir holds the <pid>.json files. Empty disables lookups entirely,
	// which is what a machine without Claude Code state looks like.
	Dir string
	// ProcStart returns pid's kernel start time in the units Claude
	// records, and false when it can't be read. Nil uses the /proc
	// reader; tests substitute their own.
	ProcStart func(pid int) (string, bool)
}

// DefaultClaudeSessions reads ~/.claude/sessions. No home directory
// leaves Dir empty — lookups then simply never find anything.
func DefaultClaudeSessions() *ClaudeSessions {
	home, err := os.UserHomeDir()
	if err != nil {
		return &ClaudeSessions{}
	}
	return &ClaudeSessions{Dir: filepath.Join(home, ".claude", "sessions")}
}

// Lookup returns the state Claude published for pid, or nil if there is
// none to trust. A file outlives a SIGKILLed process, so when the kernel
// has since recycled its pid the recorded start time won't match the
// live one and the file is ignored.
func (c *ClaudeSessions) Lookup(pid int) *ClaudeState {
	if c == nil || c.Dir == "" || pid <= 0 {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(c.Dir, strconv.Itoa(pid)+".json"))
	if err != nil {
		return nil
	}
	var f claudeStateFile
	if json.Unmarshal(raw, &f) != nil || f.SessionID == "" {
		return nil
	}
	if f.ProcStart != "" {
		procStart := c.ProcStart
		if procStart == nil {
			procStart = procStartTime
		}
		// Only a positive mismatch rejects: an unreadable start time
		// (not Linux, permissions) shouldn't cost us the status.
		if got, ok := procStart(pid); ok && got != f.ProcStart {
			return nil
		}
	}
	st := &ClaudeState{
		SessionID: f.SessionID, Name: f.Name, Status: f.Status,
		Version: f.Version, CWD: f.CWD,
	}
	if f.StatusUpdatedAt != 0 {
		st.StatusSince = time.UnixMilli(f.StatusUpdatedAt)
	}
	return st
}

// procStartTime reads pid's start time — field 22 of /proc/<pid>/stat.
// Field 2 is the executable name in parens and can itself contain
// spaces and parens, so counting starts after its closing paren.
func procStartTime(pid int) (string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return "", false
	}
	fields := strings.Fields(string(raw)[i+1:]) // fields[0] is field 3
	if len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}

// AttachClaudeState fills in each pane's Claude field. Panes with no pid
// are skipped; panes whose process publishes nothing keep a nil Claude
// and fall back to their title.
func AttachClaudeState(panes []Pane, lookup func(pid int) *ClaudeState) {
	if lookup == nil {
		return
	}
	for i := range panes {
		if panes[i].PID <= 0 {
			continue
		}
		panes[i].Claude = lookup(panes[i].PID)
	}
}
