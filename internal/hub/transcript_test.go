package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real transcript lines (Claude Code 2.1.219), trimmed to the fields the
// reader looks at.
const (
	lineAssistant = `{"type":"assistant","isSidechain":false,"effort":"high","message":` +
		`{"model":"claude-opus-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1215,` +
		`"cache_read_input_tokens":30471,"output_tokens":236}}}`
	lineUser      = `{"type":"user","isSidechain":false,"message":{"role":"user"}}`
	lineSidechain = `{"type":"assistant","isSidechain":true,"effort":"low","message":` +
		`{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":9,` +
		`"cache_read_input_tokens":999999,"output_tokens":5}}}`
)

// writeTranscript lays out <dir>/<slug>/<sessionID>.jsonl for cwd.
func writeTranscript(t *testing.T, cwd, sessionID string, lines ...string) *Transcripts {
	t.Helper()
	dir := t.TempDir()
	proj := filepath.Join(dir, projectSlug(cwd))
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(proj, sessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Transcripts{Dir: dir}
}

func TestTranscriptStats(t *testing.T) {
	tr := writeTranscript(t, "/home/user/coop", "abc", lineUser, lineAssistant)
	got, ok := tr.Stats("abc", "/home/user/coop")
	if !ok {
		t.Fatal("Stats should find the transcript")
	}
	want := TranscriptStats{Context: 2 + 1215 + 30471, Model: "claude-opus-5", Effort: "high"}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// Subagent turns carry their own context, which has nothing to do with
// the main thread's — counting them would make the column jump around.
func TestTranscriptStatsIgnoresSidechain(t *testing.T) {
	tr := writeTranscript(t, "/home/user/coop", "abc", lineAssistant, lineSidechain)
	got, ok := tr.Stats("abc", "/home/user/coop")
	if !ok {
		t.Fatal("Stats should find the transcript")
	}
	if got.Model != "claude-opus-5" || got.Context != 2+1215+30471 {
		t.Fatalf("sidechain turn should be skipped, got %+v", got)
	}
}

func TestTranscriptStatsMissing(t *testing.T) {
	tr := writeTranscript(t, "/home/user/coop", "abc", lineAssistant)
	if _, ok := tr.Stats("nosuch", "/home/user/coop"); ok {
		t.Error("a session with no transcript should report no stats")
	}
	// Nor does an unknown id resolve via the by-id fallback.
	if _, ok := tr.Stats("nosuch", "/some/other/place"); ok {
		t.Error("an unknown session should report no stats whatever the cwd")
	}
}

// A session with no assistant turn yet (just started) has no context to
// report — better a blank column than a zero that looks like a reading.
func TestTranscriptStatsNoAssistantTurn(t *testing.T) {
	tr := writeTranscript(t, "/home/user/coop", "abc", lineUser, lineUser)
	if _, ok := tr.Stats("abc", "/home/user/coop"); ok {
		t.Error("no assistant turn should report no stats")
	}
}

func TestTranscriptStatsSkipsMalformedLines(t *testing.T) {
	tr := writeTranscript(t, "/home/user/coop", "abc", lineAssistant, `{"type":"assis`)
	got, ok := tr.Stats("abc", "/home/user/coop")
	if !ok || got.Model != "claude-opus-5" {
		t.Fatalf("a half-written trailing line should be skipped, got %+v ok=%v", got, ok)
	}
}

// The transcript is appended to constantly and polled once a second, so
// an unchanged file must not be re-parsed.
func TestTranscriptStatsCachesOnModTime(t *testing.T) {
	cwd, id := "/home/user/coop", "abc"
	tr := writeTranscript(t, cwd, id, lineUser, lineAssistant)
	path := filepath.Join(tr.Dir, projectSlug(cwd), id+".jsonl")
	first, _ := tr.Stats(id, cwd)

	// Rewrite with a different model, same byte count, same mtime — the
	// cache keys on mtime+size, so an equal-length edit must not show.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	swapped := strings.Replace(lineAssistant, "claude-opus-5", "claude-opus-4", 1)
	if err := os.WriteFile(path, []byte(lineUser+"\n"+swapped+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got, _ := tr.Stats(id, cwd); got != first {
		t.Errorf("unchanged mtime/size should serve the cache: got %+v, want %+v", got, first)
	}

	// Touch it and the new content is picked up.
	if err := os.Chtimes(path, st.ModTime().Add(time.Second), st.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := tr.Stats(id, cwd); got.Model != "claude-opus-4" {
		t.Errorf("a newer mtime should re-read: got %+v", got)
	}
}

// Transcripts are megabytes; only the tail is read, so the last turn
// wins even when the file is far larger than the tail window.
func TestTranscriptStatsReadsTailOnly(t *testing.T) {
	early := strings.Replace(lineAssistant, "claude-opus-5", "claude-opus-4-8", 1)
	lines := []string{early}
	for len(strings.Join(lines, "\n")) < 3*tailBytes {
		lines = append(lines, lineUser)
	}
	lines = append(lines, lineAssistant)
	tr := writeTranscript(t, "/home/user/coop", "abc", lines...)
	got, ok := tr.Stats("abc", "/home/user/coop")
	if !ok || got.Model != "claude-opus-5" {
		t.Fatalf("the last turn should win in a large file, got %+v ok=%v", got, ok)
	}
}

// Claude derives the project directory from the cwd, but a session can
// outlive a rename; the sessionId still names the file, so fall back to
// searching for it.
func TestTranscriptStatsFindsMovedProject(t *testing.T) {
	tr := writeTranscript(t, "/home/user/oldname", "abc", lineAssistant)
	got, ok := tr.Stats("abc", "/home/user/newname")
	if !ok || got.Model != "claude-opus-5" {
		t.Fatalf("should find the transcript by session id, got %+v ok=%v", got, ok)
	}
}

func TestAttachTranscriptStats(t *testing.T) {
	panes := []Pane{
		{ID: "%1", Claude: &ClaudeState{SessionID: "abc", CWD: "/home/user/coop"}},
		{ID: "%2", Claude: &ClaudeState{SessionID: "none"}},
		{ID: "%3"}, // no published state: no way to find a transcript
	}
	var asked []string
	AttachTranscriptStats(panes, func(id, cwd string) (TranscriptStats, bool) {
		asked = append(asked, id)
		if id == "abc" {
			return TranscriptStats{Context: 100, Model: "claude-opus-5"}, true
		}
		return TranscriptStats{}, false
	})
	if panes[0].Stats == nil || panes[0].Stats.Context != 100 {
		t.Errorf("pane %%1 should carry its stats, got %+v", panes[0].Stats)
	}
	if panes[1].Stats != nil || panes[2].Stats != nil {
		t.Errorf("panes without stats should stay nil, got %+v %+v", panes[1].Stats, panes[2].Stats)
	}
	if len(asked) != 2 {
		t.Errorf("only panes with a session id should be looked up, got %v", asked)
	}
}

func TestProjectSlug(t *testing.T) {
	if got := projectSlug("/home/user/Documents/coop/git/coop"); got != "-home-user-Documents-coop-git-coop" {
		t.Errorf("projectSlug = %q", got)
	}
}
