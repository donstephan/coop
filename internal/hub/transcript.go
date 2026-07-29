package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TranscriptStats is what one session's transcript says at a glance.
// Like ClaudeState this reads undocumented internal files (shape seen on
// 2.1.219) — everything is best-effort and an unreadable transcript just
// leaves the column blank.
type TranscriptStats struct {
	// Context is the prompt size of the last assistant turn: input +
	// cache-read + cache-creation tokens. It lags the live conversation
	// by a turn, and there is no published limit to turn it into a
	// percentage (sessions here run both 200k and 1M windows).
	Context int
	Model   string // e.g. "claude-opus-5"
	Effort  string // low | medium | high | xhigh | max
}

// tailBytes is how much of a transcript's end is read. The last
// assistant turn is a few KB back even after a long tool result, and
// these files reach megabytes.
const tailBytes = 256 << 10

// Transcripts reads ~/.claude/projects/<slug>/<sessionId>.jsonl, caching
// per session so a 1-second poll doesn't re-parse unchanged files.
type Transcripts struct {
	// Dir holds the per-project directories. Empty disables lookups.
	Dir string

	mu    sync.Mutex
	cache map[string]transcriptEntry
}

// transcriptEntry is one session's cached parse, valid while the file's
// mtime and size are unchanged.
type transcriptEntry struct {
	path  string
	mod   time.Time
	size  int64
	stats TranscriptStats
	ok    bool
}

// DefaultTranscripts reads ~/.claude/projects. No home directory leaves
// Dir empty, which disables lookups.
func DefaultTranscripts() *Transcripts {
	home, err := os.UserHomeDir()
	if err != nil {
		return &Transcripts{}
	}
	return &Transcripts{Dir: filepath.Join(home, ".claude", "projects")}
}

// projectSlug is how Claude Code names a project directory: the cwd with
// its separators flattened.
func projectSlug(cwd string) string {
	return strings.ReplaceAll(cwd, string(filepath.Separator), "-")
}

// Stats returns the session's transcript stats, or false when there is
// nothing to report. cwd only hints at the project directory; the
// session id is what actually identifies the file.
func (t *Transcripts) Stats(sessionID, cwd string) (TranscriptStats, bool) {
	if t == nil || t.Dir == "" || sessionID == "" {
		return TranscriptStats{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, cached := t.cache[sessionID]
	path := entry.path
	if !cached {
		path = t.find(sessionID, cwd)
		if path == "" {
			return TranscriptStats{}, false
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		// Renamed or removed since we cached it: look again next poll.
		delete(t.cache, sessionID)
		return TranscriptStats{}, false
	}
	if cached && info.ModTime().Equal(entry.mod) && info.Size() == entry.size {
		return entry.stats, entry.ok
	}
	stats, ok := tailStats(path)
	if t.cache == nil {
		t.cache = map[string]transcriptEntry{}
	}
	t.cache[sessionID] = transcriptEntry{
		path: path, mod: info.ModTime(), size: info.Size(), stats: stats, ok: ok,
	}
	return stats, ok
}

// find locates a session's transcript: the cwd's project directory
// first, then a scan for the file by name — a session outlives a
// directory rename, and its id is stable.
func (t *Transcripts) find(sessionID, cwd string) string {
	name := sessionID + ".jsonl"
	if cwd != "" {
		p := filepath.Join(t.Dir, projectSlug(cwd), name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	matches, err := filepath.Glob(filepath.Join(t.Dir, "*", name))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// transcriptLine is the subset of a transcript record we read.
type transcriptLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Effort      string `json:"effort"`
	Message     struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens         int `json:"input_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// tailLines reads the last tailBytes of the file as whole JSONL lines
// (the leading fragment of a mid-record cut is dropped).
func tailLines(path string) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	start := info.Size() - tailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, false
	}
	buf := make([]byte, info.Size()-start)
	n, err := readFull(f, buf)
	if err != nil {
		return nil, false
	}
	lines := strings.Split(string(buf[:n]), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines, true
}

// tailStats reads the end of a transcript and reports its last main-
// thread assistant turn. Subagent turns (isSidechain) carry their own
// unrelated context and are skipped.
func tailStats(path string) (TranscriptStats, bool) {
	lines, ok := tailLines(path)
	if !ok {
		return TranscriptStats{}, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var l transcriptLine
		if json.Unmarshal([]byte(lines[i]), &l) != nil {
			continue // blank, truncated, or a record shape we don't know
		}
		if l.Type != "assistant" || l.IsSidechain || l.Message.Model == "" {
			continue
		}
		u := l.Message.Usage
		return TranscriptStats{
			Context: u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens,
			Model:   l.Message.Model,
			Effort:  l.Effort,
		}, true
	}
	return TranscriptStats{}, false
}

// LastText returns the text of the last main-thread assistant turn that
// said anything — tool-only turns are skipped, so this is what the
// session most recently told its user. Uncached: callers are episodic
// (coop peek), not the 1-second poll.
func (t *Transcripts) LastText(sessionID, cwd string) (string, bool) {
	if t == nil || t.Dir == "" || sessionID == "" {
		return "", false
	}
	t.mu.Lock()
	path := t.find(sessionID, cwd)
	t.mu.Unlock()
	if path == "" {
		return "", false
	}
	lines, ok := tailLines(path)
	if !ok {
		return "", false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var l transcriptLine
		if json.Unmarshal([]byte(lines[i]), &l) != nil {
			continue
		}
		if l.Type != "assistant" || l.IsSidechain {
			continue
		}
		var parts []string
		for _, c := range l.Message.Content {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), true
		}
	}
	return "", false
}

// AttachTranscriptStats fills in each pane's Stats from stats, which is
// keyed by the session id Claude published (so panes without published
// state are skipped — there is no way to find their transcript).
func AttachTranscriptStats(panes []Pane, stats func(sessionID, cwd string) (TranscriptStats, bool)) {
	if stats == nil {
		return
	}
	for i := range panes {
		p := &panes[i]
		if p.Claude == nil || p.Claude.SessionID == "" {
			continue
		}
		if s, ok := stats(p.Claude.SessionID, p.Claude.CWD); ok {
			p.Stats = &s
		}
	}
}

// readFull fills buf, tolerating short reads.
func readFull(f *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		total += n
		if err != nil {
			if total > 0 {
				return total, nil
			}
			return total, err
		}
		if n == 0 {
			break
		}
	}
	return total, nil
}
