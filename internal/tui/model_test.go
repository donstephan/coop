package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"coop/internal/hub"
)

type fakeTmux struct {
	panes    []hub.Pane
	screen   string
	cmd      string
	err      error
	sent     [][]string
	selected []string

	splitID        string
	splits         []string
	respawns       [][2]string
	killed         []string
	killedSessions []string
	paneOpts       map[string]string
	unknownOpts    []string          // pane options this "tmux" rejects, as an old server would
	sessionOpts    map[string]string // "session/name" -> value
	marked         string
	created        [][3]string // {name, dir, cmd}

	windowOpts map[string]string // "session/name" -> value
	serverOpts map[string]string // name -> value
	titles     [][2]string       // {pane, title} per SetPaneTitle call

	paneW, paneH int      // what PaneSize returns
	createdSizes [][2]int // {width, height} per NewSession call

	primary    bool     // what HasPrimaryClient returns
	activePane string   // what ActivePane returns
	resizes    []string // "session WxH" per ResizeWindow call

	paneResizes []string // "pane width" per ResizePane call
}

func (f *fakeTmux) CapturePane(pane string) (string, error) { return f.screen, f.err }
func (f *fakeTmux) SendKeys(pane string, keys ...string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, keys)
	return nil
}
func (f *fakeTmux) PaneCommand(pane string) (string, error) { return f.cmd, f.err }

// ListSessions materializes the option store into the pane fields the
// real paneFormat would carry — the fake plays the tmux server's role
// in the shared done-state protocol.
func (f *fakeTmux) ListSessions() ([]hub.Pane, error) {
	panes := slices.Clone(f.panes)
	for i := range panes {
		p := &panes[i]
		p.WorkingMark = f.paneOpts[p.ID+"/"+hub.WorkingMarker] == "1"
		p.DoneSince = time.Time{}
		if v := f.paneOpts[p.ID+"/"+hub.DoneSinceMarker]; v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				p.DoneSince = time.Unix(n, 0)
			}
		}
	}
	return panes, f.err
}
func (f *fakeTmux) SelectPane(pane string) error {
	if f.err != nil {
		return f.err
	}
	f.selected = append(f.selected, pane)
	return nil
}
func (f *fakeTmux) NewSession(name, dir, cmd string, width, height int) error {
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, [3]string{name, dir, cmd})
	f.createdSizes = append(f.createdSizes, [2]int{width, height})
	return nil
}
func (f *fakeTmux) PaneSize(pane string) (int, int, error) {
	return f.paneW, f.paneH, f.err
}
func (f *fakeTmux) SplitWindow(target, cmd string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.splits = append(f.splits, target)
	return f.splitID, nil
}
func (f *fakeTmux) RespawnPane(pane, cmd string) error {
	if f.err != nil {
		return f.err
	}
	f.respawns = append(f.respawns, [2]string{pane, cmd})
	return nil
}
func (f *fakeTmux) KillPane(pane string) error {
	if f.err != nil {
		return f.err
	}
	f.killed = append(f.killed, pane)
	return nil
}
func (f *fakeTmux) KillSession(name string) error {
	if f.err != nil {
		return f.err
	}
	f.killedSessions = append(f.killedSessions, name)
	return nil
}
func (f *fakeTmux) SetPaneOption(pane, name, value string) error {
	if f.err != nil {
		return f.err
	}
	if slices.Contains(f.unknownOpts, name) {
		return fmt.Errorf("invalid option: %s", name)
	}
	if f.paneOpts == nil {
		f.paneOpts = map[string]string{}
	}
	f.paneOpts[pane+"/"+name] = value
	return nil
}
func (f *fakeTmux) UnsetPaneOption(pane, name string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.paneOpts, pane+"/"+name)
	return nil
}
func (f *fakeTmux) SetSessionOption(session, name, value string) error {
	if f.err != nil {
		return f.err
	}
	if f.sessionOpts == nil {
		f.sessionOpts = map[string]string{}
	}
	f.sessionOpts[session+"/"+name] = value
	return nil
}
func (f *fakeTmux) SetWindowOption(session, name, value string) error {
	if f.err != nil {
		return f.err
	}
	if f.windowOpts == nil {
		f.windowOpts = map[string]string{}
	}
	f.windowOpts[session+"/"+name] = value
	return nil
}

func (f *fakeTmux) SetServerOption(name, value string) error {
	if f.err != nil {
		return f.err
	}
	if f.serverOpts == nil {
		f.serverOpts = map[string]string{}
	}
	f.serverOpts[name] = value
	return nil
}

func (f *fakeTmux) SetPaneTitle(pane, title string) error {
	if f.err != nil {
		return f.err
	}
	f.titles = append(f.titles, [2]string{pane, title})
	return nil
}

func (f *fakeTmux) FindMarkedPane(session, option string) (string, error) {
	return f.marked, f.err
}
func (f *fakeTmux) ResizeWindow(session string, width, height int) error {
	if f.err != nil {
		return f.err
	}
	f.resizes = append(f.resizes, fmt.Sprintf("%s %dx%d", session, width, height))
	return nil
}
func (f *fakeTmux) HasPrimaryClient(session string) (bool, error) {
	return f.primary, f.err
}
func (f *fakeTmux) ActivePane(session string) (string, error) {
	return f.activePane, f.err
}
func (f *fakeTmux) ResizePane(pane string, width int) error {
	if f.err != nil {
		return f.err
	}
	f.paneResizes = append(f.paneResizes, fmt.Sprintf("%s %d", pane, width))
	return nil
}

func testPanes() []hub.Pane {
	return []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "alpha", ID: "%1", Title: "✳ Claude Code", Cmd: "claude", Path: "/repos/alpha-repo"},
		{Session: "beta", ID: "%2", Title: "🔔 permission needed", Cmd: "claude", Path: "/repos/beta-repo"},
	}
}

// drive runs one poll round-trip: execute the poll command the model
// issued and feed its message back through Update.
func drive(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command, got nil")
	}
	next, _ := m.Update(cmd())
	return next.(Model)
}

func pollOnce(t *testing.T, f *fakeTmux) Model {
	t.Helper()
	m := New(f, []string{"claude", "node"}, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	return drive(t, m, m.poll())
}

// driveCmd is drive() but hands back the command Update returned, for
// chaining ensure → retarget flows.
func driveCmd(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command, got nil")
	}
	next, out := m.Update(cmd())
	return next.(Model), out
}

// livePanes is testPanes plus the live preview pane the fake "created".
func livePanes() []hub.Pane {
	return append(testPanes(),
		hub.Pane{Session: "roost", ID: "%50", Title: "live", Cmd: "tmux", Hub: true})
}

// bootLive drives a full startup: poll → ensure live pane → retarget.
// Returns the settled model.
func bootLive(t *testing.T, f *fakeTmux) Model {
	t.Helper()
	m := New(f, []string{"claude", "node"}, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m, cmd := driveCmd(t, m, m.poll()) // pollMsg → ensure cmd
	m, cmd = driveCmd(t, m, cmd)       // livePaneMsg → retarget cmd
	m, _ = driveCmd(t, m, cmd)         // retargetMsg
	return m
}

// pollMsgNow executes the model's poll command and returns its message.
func (m Model) pollMsgNow(t *testing.T) tea.Msg {
	t.Helper()
	return m.poll()()
}

// Claude Code publishes its status per pid; the poll joins on pane_pid
// and that beats the pane title (idle-looking here, actually busy).
func TestPollUsesClaudeState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "4242.json"),
		[]byte(`{"sessionId":"abc","name":"coop-fd","status":"busy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "coop", ID: "%1", PID: 4242, Title: "✻ coop", Cmd: "claude"},
		{Session: "other", ID: "%2", PID: 99, Title: "⠂ working", Cmd: "claude"},
	}}
	m := New(f, []string{"claude", "node"}, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.claude = &hub.ClaudeSessions{Dir: dir}
	m = drive(t, m, m.poll())
	if m.panes[0].Status != hub.StatusWorking {
		t.Errorf("pane with published state: Status = %v, want working", m.panes[0].Status)
	}
	if m.panes[0].Claude == nil || m.panes[0].Claude.Name != "coop-fd" {
		t.Errorf("pane should carry its published state, got %+v", m.panes[0].Claude)
	}
	// No file for pid 99: that pane still reads its title.
	if m.panes[1].Status != hub.StatusWorking || m.panes[1].Claude != nil {
		t.Errorf("pane without published state: %v %+v", m.panes[1].Status, m.panes[1].Claude)
	}
}

func TestNewReadsDefaultClaudeSessions(t *testing.T) {
	m := New(&fakeTmux{}, nil, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	if m.claude == nil {
		t.Fatal("New should wire up the default ~/.claude/sessions reader")
	}
}

func TestPollFiltersHubAndSorts(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	if len(m.panes) != 2 {
		t.Fatalf("hub session should be filtered; got %d panes", len(m.panes))
	}
	if m.panes[0].Session != "alpha" {
		t.Fatalf("groups sort alphabetically — beta's needs-input must not hoist it, got %q",
			m.panes[0].Session)
	}
	if m.selectedID != "%1" {
		t.Fatalf("first poll should select the top pane, got %q", m.selectedID)
	}
}

// Every session marked @coop is another hub instance and stays out
// of the list — but a repo session that happens to start in a directory
// named like a hub is only hidden if actually marked.
func TestPollFiltersAllMarkedHubs(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "roost-2", ID: "%10", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "roost-3", ID: "%11", Title: "✳ Claude Code", Cmd: "claude", Path: "/repos/roost"},
	}}
	m := pollOnce(t, f)
	if len(m.panes) != 1 || m.panes[0].ID != "%11" {
		t.Fatalf("only the unmarked repo session should remain, got %+v", m.panes)
	}
}

// The collision set for naming new sessions must cover every hub
// instance on the socket, not just this one — a repo named "roost-2"
// must not try to claim another hub's session name.
func TestSessionNamesIncludeAllHubs(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "roost-2", ID: "%10", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "alpha", ID: "%1", Title: "✳ Claude Code", Cmd: "claude", Path: "/repos/alpha-repo"},
	}}
	names := pollOnce(t, f).sessionNames()
	for _, want := range []string{"roost", "roost-2", "alpha"} {
		if !slices.Contains(names, want) {
			t.Fatalf("sessionNames() = %v, missing %q", names, want)
		}
	}
}

func TestViewShowsSessionsAndStatus(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	view := m.View()
	for _, want := range []string{"alpha-repo", "beta-repo",
		"○  Claude Code", "◆  permission needed"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

// Rows group under repo headers in alphabetical order — needs-input
// does not reorder them — and titles lose their leading status glyph.
func TestViewGroupsByRepo(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	view := m.View()
	ai, bi := strings.Index(view, "alpha-repo"), strings.Index(view, "beta-repo")
	if ai < 0 || bi < 0 || ai > bi {
		t.Errorf("alpha-repo should render before beta-repo (needs input must not hoist):\n%s", view)
	}
	if strings.Contains(view, "🔔") || strings.Contains(view, "✳") {
		t.Errorf("status glyphs should be stripped from titles:\n%s", view)
	}
}

// The time column ages from the current status, not from session start:
// a session that has run for hours but only just went idle reads "4m".
func TestViewShowsStatusAgeNotSessionAge(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	f := &fakeTmux{panes: []hub.Pane{{
		Session: "alpha", ID: "%1", Title: "✳ Claude Code", Cmd: "claude",
		Path: "/repos/alpha-repo", Created: old,
	}}}
	m := pollOnce(t, f)
	if !strings.Contains(m.View(), "3h00m") {
		t.Fatalf("without published state the row should age from session start:\n%s", m.View())
	}

	m.panes[0].Claude = &hub.ClaudeState{
		Status: "idle", StatusSince: time.Now().Add(-4 * time.Minute)}
	view := m.View()
	if !strings.Contains(view, "4m") || strings.Contains(view, "3h00m") {
		t.Errorf("row should age from the published status (4m), not the session (3h00m):\n%s", view)
	}
}

func TestLiveTitleUsesStatusAge(t *testing.T) {
	panes := []hub.Pane{{
		ID: "%1", Title: "✻ alpha", Created: time.Now().Add(-3 * time.Hour),
		Claude: &hub.ClaudeState{Status: "busy", StatusSince: time.Now().Add(-4 * time.Minute)},
	}}
	hub.DeriveStatuses(panes, nil)
	if got := liveTitleFor(panes, "%1"); got != "working · 4m · alpha" {
		t.Errorf("liveTitleFor = %q, want %q", got, "working · 4m · alpha")
	}
}

// Each status gets its own shape, so a glance doesn't have to tell blue
// from yellow — colour reinforces the marker instead of carrying it.
func TestStatusGlyph(t *testing.T) {
	want := map[hub.Status]string{
		hub.StatusNeedsInput: "◆",
		hub.StatusWorking:    "●",
		hub.StatusDone:       "✓",
		hub.StatusIdle:       "○",
	}
	seen := map[string]hub.Status{}
	for st, w := range want {
		got := statusGlyph(st)
		if got != w {
			t.Errorf("statusGlyph(%v) = %q, want %q", st, got, w)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("statusGlyph(%v) and (%v) both render %q", st, prev, got)
		}
		seen[got] = st
		// The row pads by display width; a two-cell glyph would shear
		// every column to its right.
		if w := lipgloss.Width(got); w != 1 {
			t.Errorf("statusGlyph(%v) = %q is %d cells wide, want 1", st, got, w)
		}
	}
}

func TestModelBadge(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5":             "o5",
		"claude-fable-5":            "f5",
		"claude-opus-4-8":           "o4.8",
		"claude-sonnet-5":           "s5",
		"claude-haiku-4-5-20251001": "h4.5",
		"something-new-7":           "something",
		"":                          "",
	}
	for in, want := range cases {
		if got := modelBadge(in); got != want {
			t.Errorf("modelBadge(%q) = %q, want %q", in, got, want)
		}
	}
}

// The five levels `claude --effort` accepts, verified against 2.1.219.
func TestEffortBadge(t *testing.T) {
	cases := map[string]string{
		"low": "lo", "medium": "md", "high": "hi", "xhigh": "xh", "max": "mx",
		"": "", "weird": "",
	}
	for in, want := range cases {
		if got := effortBadge(in); got != want {
			t.Errorf("effortBadge(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		0: "", 940: "940", 30256: "30k", 136517: "137k", 352885: "353k",
		999_499: "999k", 1_251_204: "1.3M",
	}
	for in, want := range cases {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

// s cycles the extra column off → context → model → off.
func TestStatColumnCycles(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	if m.statCol != statColOff {
		t.Fatalf("stat column should start off, got %v", m.statCol)
	}
	for _, want := range []statColumn{statColContext, statColModel, statColOff} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		m = next.(Model)
		if m.statCol != want {
			t.Fatalf("after s: statCol = %v, want %v", m.statCol, want)
		}
	}
}

func TestViewShowsStatColumn(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{{
		Session: "alpha", ID: "%1", Title: "✳ Claude Code", Cmd: "claude",
		Path: "/repos/alpha-repo",
	}}}
	m := pollOnce(t, f)
	m.panes[0].Stats = &hub.TranscriptStats{
		Context: 136517, Model: "claude-opus-5", Effort: "high"}

	if v := m.View(); strings.Contains(v, "137k") || strings.Contains(v, "o5·hi") {
		t.Errorf("stat column is off; nothing should render:\n%s", v)
	}
	m.statCol = statColContext
	if v := m.View(); !strings.Contains(v, "137k") {
		t.Errorf("context column missing:\n%s", v)
	}
	m.statCol = statColModel
	if v := m.View(); !strings.Contains(v, "o5·hi") {
		t.Errorf("model column missing:\n%s", v)
	}
}

// Token counts are right-aligned so the column reads as numbers; model
// badges are left-aligned so they read as labels.
func TestStatTextAlignment(t *testing.T) {
	p := hub.Pane{Stats: &hub.TranscriptStats{
		Context: 30256, Model: "claude-opus-5", Effort: "high"}}
	if got := statText(p, statColContext); got != " 30k" {
		t.Errorf("context cell = %q, want %q (right-aligned)", got, " 30k")
	}
	if got := statText(p, statColModel); got != "o5·hi" {
		t.Errorf("model cell = %q, want %q (unpadded)", got, "o5·hi")
	}
	if got := statText(hub.Pane{}, statColContext); got != "" {
		t.Errorf("no stats should be blank, not padded, got %q", got)
	}
}

// A session with no transcript yet leaves the column blank rather than
// rendering a zero that reads like a measurement.
func TestViewStatColumnBlankWithoutStats(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	m.statCol = statColContext
	if v := m.View(); strings.Contains(v, "0k") {
		t.Errorf("missing stats should render blank, not 0k:\n%s", v)
	}
}

// Turning the column on widens the nav pane; tmux has to be told.
func TestStatColumnResizesNav(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	m.selfPane, m.width = "%9", navWidth
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("turning the column on should resize the nav pane")
	}
	cmd()
	want := fmt.Sprintf("%%9 %d", navWidth+statWidth)
	if len(f.paneResizes) != 1 || f.paneResizes[0] != want {
		t.Fatalf("paneResizes = %v, want [%s]", f.paneResizes, want)
	}
}

// Reading transcripts is file I/O on every poll tick — don't do it for a
// column nobody is looking at.
func TestPollSkipsTranscriptsWhenColumnOff(t *testing.T) {
	// A transcript is found via the session id in the pane's published
	// state, so the pane needs one. PID 0 means AttachClaudeState skips
	// the pane and leaves the state the fake supplied.
	panes := testPanes()
	panes[1].Claude = &hub.ClaudeState{SessionID: "abc", Status: "idle"}
	f := &fakeTmux{panes: panes}
	m := New(f, []string{"claude"}, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	asked := 0
	m.transcripts = func(sessionID, cwd string) (hub.TranscriptStats, bool) {
		asked++
		return hub.TranscriptStats{}, false
	}
	m = drive(t, m, m.poll())
	if asked != 0 {
		t.Fatalf("column off: %d transcript reads, want 0", asked)
	}
	m.statCol = statColContext
	m = drive(t, m, m.poll())
	if asked == 0 {
		t.Fatal("column on: transcripts should be read")
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"✳ Claude Code": "Claude Code",
		"✻ sprocket-v2": "sprocket-v2",
		"⠂ compiling":   "compiling",
		"🔔 pick one":    "pick one",
		"plain":         "plain",
		"":              "",
		"✳":             "",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Errorf("truncate = %q, want abc…", got)
	}
	if got := truncate("abcd", 4); got != "abcd" {
		t.Errorf("truncate should not touch fitting strings, got %q", got)
	}
}

// A resize away from navWidth pins the nav pane back; the resulting
// WindowSizeMsg at navWidth leaves the pane alone and falls through to
// the live-preview window sync instead.
func TestWindowSizePinsNavWidth(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), marked: "%50"}
	m := bootLive(t, f)
	m.selfPane = "%0"
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	m = next.(Model)
	m = drive(t, m, cmd)
	want := fmt.Sprintf("%%0 %d", navWidth)
	if len(f.paneResizes) != 1 || f.paneResizes[0] != want {
		t.Fatalf("paneResizes = %v, want [%s]", f.paneResizes, want)
	}
	next, cmd = m.Update(tea.WindowSizeMsg{Width: navWidth, Height: 30})
	m = next.(Model)
	if cmd != nil {
		drive(t, m, cmd)
	}
	if len(f.paneResizes) != 1 {
		t.Fatalf("nav at navWidth must not resize again, got %v", f.paneResizes)
	}
}

// The split's WindowSizeMsg races ahead of livePaneMsg (livePane is
// still "" when it lands), so the pin must also fire when the live pane
// arrives, then chain into the preview attach.
func TestLivePaneMsgPinsNavThenRetargets(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), marked: "%50"}
	m := New(f, []string{"claude", "node"}, "roost", "cc", "%0", "claude", nil, nil, 0, ArbiterConfig{})
	m, cmd := driveCmd(t, m, m.poll()) // pollMsg → ensure cmd
	m, cmd = driveCmd(t, m, cmd)       // livePaneMsg → resizeSelf cmd
	m, cmd = driveCmd(t, m, cmd)       // resizedMsg → retarget cmd
	want := fmt.Sprintf("%%0 %d", navWidth)
	if len(f.paneResizes) != 1 || f.paneResizes[0] != want {
		t.Fatalf("paneResizes = %v, want [%s]", f.paneResizes, want)
	}
	m, _ = driveCmd(t, m, cmd) // retargetMsg
	if len(f.respawns) != 1 {
		t.Fatalf("preview should still attach after the pin, got %v", f.respawns)
	}
}

// Without a live pane there is nothing to hand the freed space to — the
// nav must not shrink the only pane in the window.
func TestNoNavPinBeforeLivePane(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	m.selfPane = "%0"
	m.livePane = ""
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	if cmd != nil {
		t.Fatalf("expected no command before the live pane exists")
	}
}

func TestViewEmptyState(t *testing.T) {
	m := pollOnce(t, &fakeTmux{})
	if !strings.Contains(m.View(), "no sessions") {
		t.Errorf("empty state missing:\n%s", m.View())
	}
}

func TestCursorMovesAndSticksToPane(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f) // alpha (%1) top, selected
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.selectedID != "%2" {
		t.Fatalf("down should select beta (%%2), got %q", m.selectedID)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	if m.selectedID != "%1" {
		t.Fatalf("up should select alpha (%%1), got %q", m.selectedID)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	// beta's repo moves to a-repo, so its group now sorts above alpha's —
	// cursor must stay on beta's pane across the re-sort.
	f.panes = []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "alpha", ID: "%1", Title: "✳ Claude Code", Cmd: "claude", Path: "/repos/alpha-repo"},
		{Session: "beta", ID: "%2", Title: "🔔 permission needed", Cmd: "claude", Path: "/repos/a-repo"},
	}
	m = drive(t, m, m.poll())
	if m.selectedID != "%2" {
		t.Fatalf("cursor should stick to pane %%2 across re-sort, got %q", m.selectedID)
	}
	if m.panes[0].ID != "%2" {
		t.Fatalf("a-repo should now sort first, got %q", m.panes[0].ID)
	}
}

func TestClickSelectsSessionRow(t *testing.T) {
	click := func(y int) tea.MouseMsg {
		return tea.MouseMsg{X: 2, Y: y,
			Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	}
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f) // alpha (%1) top, selected
	// Rows: border(0), "coop  2 sessions"(1), blank(2), alpha-repo(3),
	// alpha(4), beta-repo(5), beta(6).
	next, _ := m.Update(click(6))
	m = next.(Model)
	if m.selectedID != "%2" {
		t.Fatalf("click on beta's row should select %%2, got %q", m.selectedID)
	}
	next, _ = m.Update(click(4))
	m = next.(Model)
	if m.selectedID != "%1" {
		t.Fatalf("click on alpha's row should select %%1, got %q", m.selectedID)
	}
	for _, y := range []int{0, 1, 2, 3, 5, 7, 40} {
		next, _ = m.Update(click(y))
		m = next.(Model)
		if m.selectedID != "%1" {
			t.Fatalf("click on non-session row %d should not move selection, got %q",
				y, m.selectedID)
		}
	}
	next, _ = m.Update(tea.MouseMsg{X: 2, Y: 6,
		Action: tea.MouseActionPress, Button: tea.MouseButtonRight})
	m = next.(Model)
	if m.selectedID != "%1" {
		t.Fatalf("right click should not move selection, got %q", m.selectedID)
	}
}

func TestClickRestoresFocusHighlight(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", activePane: "%50"}
	m := bootLive(t, f)
	next, cmd := m.Update(tea.BlurMsg{})
	m = next.(Model)
	drive(t, m, cmd)
	// tmux's click binding focuses the nav pane, but the focus-in escape
	// arrives glued to the forwarded click and Bubble Tea drops it — the
	// click itself must flip the focus highlight back on.
	next, cmd = m.Update(tea.MouseMsg{X: 2, Y: 6,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	if !m.focused {
		t.Fatal("click should mark the nav focused")
	}
	if m.selectedID != "%2" {
		t.Fatalf("click should still select beta (%%2), got %q", m.selectedID)
	}
	if cmd == nil {
		t.Fatal("click should issue retarget and border commands")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("click from blur should batch retarget+border, got %T", cmd())
	}
	for _, c := range batch {
		m = drive(t, m, c)
	}
	if got := f.windowOpts["roost/pane-active-border-style"]; got != "fg=colour"+hub.BorderDim {
		t.Fatalf("pane-active-border-style = %q, want dim after click", got)
	}
}

func TestClickIgnoredWhilePickingOrConfirming(t *testing.T) {
	click := tea.MouseMsg{X: 2, Y: 6,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f)
	m.confirmKill = "alpha"
	next, _ := m.Update(click)
	m = next.(Model)
	if m.selectedID != "%1" || m.confirmKill != "alpha" {
		t.Fatalf("click during kill confirm should be inert, got sel=%q confirm=%q",
			m.selectedID, m.confirmKill)
	}
	m.confirmKill = ""
	m.picking = true
	next, _ = m.Update(click)
	m = next.(Model)
	if m.selectedID != "%1" {
		t.Fatalf("click during picker should be inert, got %q", m.selectedID)
	}
}

func TestClickBelowTruncatedListIgnored(t *testing.T) {
	// A short frame truncates the body and draws the footer where deeper
	// rows would be — clicks there must not select hidden sessions.
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f)
	m.width, m.height = 60, 7 // 1-line footer: body rows are y 1-4; beta's row (y 6) is cut
	next, _ := m.Update(tea.MouseMsg{X: 2, Y: 6,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(Model)
	if m.selectedID != "%1" {
		t.Fatalf("click on footer area should not select, got %q", m.selectedID)
	}
}

// arbiterPane is the pinned arbiter row: hub.SortPanes puts it last, so
// tests that want it place it last too.
func arbiterPane(id string, needs bool) hub.Pane {
	p := hub.Pane{ID: id, Session: "arbiter", Arbiter: true, ArbiterMode: "full"}
	if needs {
		p.Status = hub.StatusNeedsInput
	}
	return p
}

func TestNextNeedsInput(t *testing.T) {
	pane := func(id string, needs bool) hub.Pane {
		p := hub.Pane{ID: id, Session: "s" + id}
		if needs {
			p.Status = hub.StatusNeedsInput
		}
		return p
	}
	cases := map[string]struct {
		panes    []hub.Pane
		selected string
		want     string
	}{
		"forward": {[]hub.Pane{pane("%1", false), pane("%2", false), pane("%3", true)}, "%1", "%3"},
		"wraps":   {[]hub.Pane{pane("%1", true), pane("%2", false), pane("%3", false)}, "%3", "%1"},
		"skips current first": {[]hub.Pane{pane("%1", false), pane("%2", true),
			pane("%3", false), pane("%4", true)}, "%2", "%4"},
		"current is last resort": {[]hub.Pane{pane("%1", false), pane("%2", true),
			pane("%3", false)}, "%2", "%2"},
		"none":              {[]hub.Pane{pane("%1", false), pane("%2", false)}, "%1", ""},
		"empty":             {nil, "%1", ""},
		"unknown selection": {[]hub.Pane{pane("%1", false), pane("%2", true)}, "%99", "%2"},
		// An arbiter at its own permission prompt is never a tab target.
		"skips arbiter": {[]hub.Pane{pane("%1", false), pane("%2", false),
			arbiterPane("%9", true)}, "%1", ""},
		"skips arbiter for a real session": {[]hub.Pane{pane("%1", false),
			arbiterPane("%9", true), pane("%3", true)}, "%1", "%3"},
	}
	for name, c := range cases {
		if got := nextNeedsInput(c.panes, c.selected); got != c.want {
			t.Errorf("%s: nextNeedsInput() = %q, want %q", name, got, c.want)
		}
	}
}

// tab moves the cursor to the next needs-input session and retargets the
// preview, exactly like ↑/↓ would.
func TestTabSelectsNextNeedsInputAndRetargets(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f) // alpha (idle) selected, previewed
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if m.selectedID != "%2" {
		t.Fatalf("tab should select beta (needs input), got %q", m.selectedID)
	}
	m, _ = driveCmd(t, m, cmd)
	if len(f.respawns) != 2 || !strings.Contains(f.respawns[1][1], "beta") {
		t.Fatalf("tab should retarget the preview to beta, got %v", f.respawns)
	}
}

// With several sessions blocked, repeated tabs cycle through them.
func TestTabCyclesThroughNeedsInput(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "alpha", ID: "%1", Title: "🔔 pick", Cmd: "claude", Path: "/repos/alpha-repo"},
		{Session: "beta", ID: "%2", Title: "✳ Claude Code", Cmd: "claude", Path: "/repos/beta-repo"},
		{Session: "gamma", ID: "%3", Title: "🔔 pick", Cmd: "claude", Path: "/repos/gamma-repo"},
	}}
	m := pollOnce(t, f) // alpha (top, needs input) selected
	for _, want := range []string{"%3", "%1", "%3"} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(Model)
		if m.selectedID != want {
			t.Fatalf("tab should cycle to %q, got %q", want, m.selectedID)
		}
	}
}

// With nothing blocked, tab stays put and says so in the footer.
func TestTabWithoutNeedsInputShowsNotice(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "alpha", ID: "%1", Title: "✳ Claude Code", Cmd: "claude", Path: "/repos/alpha-repo"},
	}}
	m := pollOnce(t, f)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("tab with nothing blocked must not issue a command")
	}
	if m.selectedID != "%1" {
		t.Fatalf("selection must not move, got %q", m.selectedID)
	}
	if m.actionErr != "no sessions need input" {
		t.Fatalf("actionErr = %q, want notice", m.actionErr)
	}
}

// The footer starts as a single hint line; ? expands the full key list
// (including the tmux-level shift+arrow pane switch) and ? collapses it
// again. The picker ignores ? entirely.
func TestHelpToggle(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	v := m.View()
	if !strings.Contains(v, "? help") {
		t.Fatalf("collapsed footer should advertise ? help:\n%s", v)
	}
	if strings.Contains(v, "tab next input") {
		t.Fatalf("collapsed footer should not list secondary keys:\n%s", v)
	}
	next, _ := m.Update(keyRunes("?"))
	m = next.(Model)
	v = m.View()
	for _, want := range []string{"tab next input", "shift+←/→ switch pane", "? close"} {
		if !strings.Contains(v, want) {
			t.Fatalf("expanded footer should list %q:\n%s", want, v)
		}
	}
	next, _ = m.Update(keyRunes("?"))
	m = next.(Model)
	if v := m.View(); strings.Contains(v, "tab next input") {
		t.Fatalf("second ? should collapse the footer:\n%s", v)
	}
}

func TestHelpToggleInertWhilePicking(t *testing.T) {
	m := newPicker(t, &fakeTmux{panes: testPanes()}, []string{"/tmp/gamma"})
	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, _ = m.Update(keyRunes("?"))
	m = next.(Model)
	if m.showHelp {
		t.Fatal("? during the picker must not toggle help")
	}
	if !m.picking {
		t.Fatal("? during the picker must not leave picker mode")
	}
}

// Terminal focus events (tmux focus-events → bubbletea report-focus)
// drive the frame colour; the model starts focused — the nav is the
// only pane at launch.
func TestFocusEventsTrackFocus(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	if !m.focused {
		t.Fatal("model should start focused")
	}
	next, _ := m.Update(tea.BlurMsg{})
	m = next.(Model)
	if m.focused {
		t.Fatal("BlurMsg should clear focused")
	}
	next, _ = m.Update(tea.FocusMsg{})
	m = next.(Model)
	if !m.focused {
		t.Fatal("FocusMsg should set focused")
	}
}

// Blur while the live pane is active means the user moved into the
// preview — the window's active-border style flips to amber so the
// preview's bar lights up.
func TestBlurWithPreviewActiveLightsBorder(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", activePane: "%50"}
	m := bootLive(t, f)
	next, cmd := m.Update(tea.BlurMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("blur should issue a border command")
	}
	drive(t, m, cmd)
	if got := f.windowOpts["roost/pane-active-border-style"]; got != "fg=colour"+hub.FocusAccent {
		t.Fatalf("pane-active-border-style = %q, want amber", got)
	}
	want := " #[fg=colour" + hub.FocusAccent + "]#{pane_title}#[default] "
	if got := f.paneOpts["%50/pane-border-format"]; got != want {
		t.Fatalf("pane-border-format = %q, want amber text %q", got, want)
	}
}

// Blur with the nav still active is the terminal window losing focus —
// no panel is focused, so nothing may light up.
func TestBlurWithNavActiveKeepsBorderDim(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", activePane: "%0"}
	m := bootLive(t, f)
	next, cmd := m.Update(tea.BlurMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("blur should issue a border command")
	}
	drive(t, m, cmd)
	if got := f.windowOpts["roost/pane-active-border-style"]; got != "fg=colour"+hub.BorderDim {
		t.Fatalf("pane-active-border-style = %q, want dim", got)
	}
	want := " #[fg=colour" + hub.TitleText + "]#{pane_title}#[default] "
	if got := f.paneOpts["%50/pane-border-format"]; got != want {
		t.Fatalf("pane-border-format = %q, want plain text %q", got, want)
	}
}

// Focus returning to the nav dims the tmux border again — amber belongs
// to the lipgloss frame now.
func TestFocusDimsBorder(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", activePane: "%50"}
	m := bootLive(t, f)
	next, cmd := m.Update(tea.BlurMsg{})
	m = next.(Model)
	drive(t, m, cmd)
	next, cmd = m.Update(tea.FocusMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("focus should issue a border command")
	}
	drive(t, m, cmd)
	if got := f.windowOpts["roost/pane-active-border-style"]; got != "fg=colour"+hub.BorderDim {
		t.Fatalf("pane-active-border-style = %q, want dim after refocus", got)
	}
	want := " #[fg=colour" + hub.TitleText + "]#{pane_title}#[default] "
	if got := f.paneOpts["%50/pane-border-format"]; got != want {
		t.Fatalf("pane-border-format = %q, want text back to plain %q", got, want)
	}
}

// The nested attach passes the inner app's title escapes through, which
// would clobber the session · status · age bar — the live pane must
// refuse title changes from its content.
func TestLivePaneRefusesInnerTitles(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	bootLive(t, f)
	if got := f.paneOpts["%50/allow-set-title"]; got != "off" {
		t.Fatalf("allow-set-title = %q, want off", got)
	}
}

// The nav renders inside a rounded frame filling the pane, its title in
// the top border and the key footer pinned to the bottom edge.
func TestViewFramedWithTitle(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	m.width, m.height = 50, 12
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 12 {
		t.Fatalf("view should fill the pane height, got %d lines", len(lines))
	}
	if !strings.Contains(lines[0], "╭") || !strings.Contains(lines[0], "sessions") {
		t.Fatalf("top border should embed the title:\n%s", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "╰") {
		t.Fatalf("bottom border missing:\n%s", lines[len(lines)-1])
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 50 {
			t.Fatalf("line %d is %d cells wide, want 50:\n%s", i, w, l)
		}
	}
	if !strings.Contains(lines[len(lines)-2], "? help") {
		t.Fatalf("key footer should pin to the bottom edge:\n%s", lines[len(lines)-2])
	}
}

func TestPickerFramedWithTitle(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/gamma"})
	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	m.width, m.height = 50, 12
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 12 {
		t.Fatalf("picker should fill the pane height, got %d lines", len(lines))
	}
	if !strings.Contains(lines[0], "new session") {
		t.Fatalf("picker title should embed in the top border:\n%s", lines[0])
	}
	if !strings.Contains(lines[len(lines)-2], "esc cancel") {
		t.Fatalf("picker footer should pin to the bottom edge:\n%s", lines[len(lines)-2])
	}
}

// Losing focus dims the frame — with a real colour profile forced, the
// focused and blurred renders must differ.
func TestBlurDimsFrame(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	old := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	defer r.SetColorProfile(old)

	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	focused := m.View()
	next, _ := m.Update(tea.BlurMsg{})
	m = next.(Model)
	if m.View() == focused {
		t.Fatal("blurred frame should render differently from focused")
	}
}

func TestPollErrorGoesToFooterNotCrash(t *testing.T) {
	m := pollOnce(t, &fakeTmux{err: errFake})
	if m.errMsg == "" {
		t.Fatal("poll error should surface in errMsg")
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "tmux exploded" }

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestEnterFocusesLivePane(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("enter should issue a focus command")
	}
	msg := cmd() // execute the focus
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.selected) != 1 || f.selected[0] != "%50" {
		t.Fatalf("expected focus on live pane %%50, got %v", f.selected)
	}
	if m.actionErr != "" {
		t.Fatalf("successful focus should not set actionErr, got %q", m.actionErr)
	}
}

func TestEnterWithoutLivePaneIsNoop(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter with no live pane should issue no command")
	}
	if len(f.selected) != 0 {
		t.Fatalf("no pane should be selected, got %v", f.selected)
	}
}

func TestActionErrorSurvivesPollAndClearsOnKeypress(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "bash"}
	m := pollOnce(t, f)

	next, _ := m.Update(sentMsg{err: fmt.Errorf("refusing to send: pane is running %q", "bash")})
	m = next.(Model)
	if m.actionErr == "" {
		t.Fatal("sentMsg error should set actionErr")
	}

	// A subsequent successful poll must not wipe the action error.
	m = drive(t, m, m.poll())
	if m.actionErr == "" {
		t.Fatal("actionErr should survive a successful poll")
	}
	if !strings.Contains(m.View(), "refusing to send") {
		t.Fatalf("action error should still render in the view:\n%s", m.View())
	}

	// The next keypress clears it.
	next, _ = m.Update(keyRunes("j"))
	m = next.(Model)
	if m.actionErr != "" {
		t.Fatalf("actionErr should clear on next keypress, got %q", m.actionErr)
	}
}

const dialogScreen = `Do you want to proceed?
❯ 1. Yes
  2. No, and tell Claude what to do differently (esc)`

func TestScreenDialogTurnsIdleIntoNeedsInput(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), screen: dialogScreen}
	m := pollOnce(t, f)
	i := m.indexOf("%1") // alpha: idle title, dialog on screen
	if i < 0 {
		t.Fatal("alpha missing")
	}
	if m.panes[i].Status != hub.StatusNeedsInput {
		t.Fatalf("alpha with on-screen dialog should be needs-input, got %v", m.panes[i].Status)
	}
	if !strings.Contains(m.View(), "◆  Claude Code") {
		t.Error("view should render alpha's derived needs-input glyph")
	}
}

func TestDigitAnswersNeedsInputSession(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "claude"}
	m := pollOnce(t, f)                                // alpha selected
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown}) // → beta (🔔)
	m = next.(Model)
	next, cmd := m.Update(keyRunes("1"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("digit on a needs-input session should issue a send command")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.sent) != 1 || len(f.sent[0]) != 1 || f.sent[0][0] != "1" {
		t.Fatalf("expected bare key '1' sent, got %v", f.sent)
	}
	if m.actionErr != "" {
		t.Fatalf("successful answer should not set actionErr, got %q", m.actionErr)
	}
}

func TestDigitPassesThroughOnIdleSession(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "claude"}
	m := pollOnce(t, f) // alpha (idle) selected
	next, cmd := m.Update(keyRunes("1"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("digit on an idle session should pass through")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.sent) != 1 || len(f.sent[0]) != 1 || f.sent[0][0] != "1" {
		t.Fatalf("expected bare key '1' sent, got %v", f.sent)
	}
	if m.actionErr != "" {
		t.Fatalf("passthrough should not set actionErr, got %q", m.actionErr)
	}
}

func TestZeroPassesThrough(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "claude"}
	m := pollOnce(t, f) // alpha selected
	next, cmd := m.Update(keyRunes("0"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("0 should pass through like the other digits")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.sent) != 1 || len(f.sent[0]) != 1 || f.sent[0][0] != "0" {
		t.Fatalf("expected bare key '0' sent, got %v", f.sent)
	}
}

func TestBackspacePassesThroughAsBSpace(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "claude"}
	m := pollOnce(t, f)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("backspace should pass through")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.sent) != 1 || len(f.sent[0]) != 1 || f.sent[0][0] != "BSpace" {
		t.Fatalf("expected tmux key name 'BSpace' sent, got %v", f.sent)
	}
}

// / starts a slash command: the key is forwarded to the selected session
// and keyboard focus jumps into the live preview so typing continues there.
func TestSlashForwardsAndFocusesPreview(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", cmd: "claude"}
	m := bootLive(t, f)
	next, cmd := m.Update(keyRunes("/"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("slash should issue a send+focus command")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.sent) != 1 || len(f.sent[0]) != 1 || f.sent[0][0] != "/" {
		t.Fatalf("expected bare key '/' sent, got %v", f.sent)
	}
	if len(f.selected) != 1 || f.selected[0] != "%50" {
		t.Fatalf("expected focus on live pane %%50, got %v", f.selected)
	}
	if m.actionErr != "" {
		t.Fatalf("successful slash should not set actionErr, got %q", m.actionErr)
	}
}

// All-or-nothing: with no live preview to focus, the / is not sent
// anywhere — an error explains instead.
func TestSlashWithoutLivePaneErrorsAndSendsNothing(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "claude"}
	m := pollOnce(t, f)
	next, cmd := m.Update(keyRunes("/"))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("slash without a live pane should issue no command")
	}
	if len(f.sent) != 0 {
		t.Fatalf("nothing should be sent, got %v", f.sent)
	}
	if m.actionErr == "" {
		t.Fatal("slash without a live pane should set actionErr")
	}
}

func TestSlashWithoutSelectionErrorsAndSendsNothing(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", cmd: "claude"}
	m := bootLive(t, f)
	m.selectedID = ""
	next, cmd := m.Update(keyRunes("/"))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("slash without a selection should issue no command")
	}
	if len(f.sent) != 0 {
		t.Fatalf("nothing should be sent, got %v", f.sent)
	}
	if m.actionErr == "" {
		t.Fatal("slash without a selection should set actionErr")
	}
}

// The running-claude gate refuses the send and skips the focus — the
// keyboard must not land in a preview showing a bare shell.
func TestSlashGatedOnAllowedCmds(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", cmd: "bash"}
	m := bootLive(t, f)
	next, cmd := m.Update(keyRunes("/"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a command (which must refuse at send time)")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.sent) != 0 {
		t.Fatalf("send must be refused for disallowed pane command, got %v", f.sent)
	}
	if len(f.selected) != 0 {
		t.Fatalf("refused send must not focus the preview, got %v", f.selected)
	}
	if !strings.Contains(m.actionErr, "bash") {
		t.Fatalf("refusal should name the pane command, got %q", m.actionErr)
	}
}

func TestExpandedHelpAdvertisesPassthroughKeys(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "claude"}
	m := pollOnce(t, f)
	next, _ := m.Update(keyRunes("?"))
	m = next.(Model)
	v := m.View()
	for _, want := range []string{"/ command", "0-9 answer", "bksp erase"} {
		if !strings.Contains(v, want) {
			t.Fatalf("expanded help should advertise %q:\n%s", want, v)
		}
	}
}

func TestDigitAnswerStillGatedOnAllowedCmds(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), cmd: "bash"}
	m := pollOnce(t, f) // alpha selected; the gate is status-independent
	next, cmd := m.Update(keyRunes("2"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a command (which must refuse at send time)")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)
	if len(f.sent) != 0 {
		t.Fatalf("send must be refused for disallowed pane command, got %v", f.sent)
	}
	if !strings.Contains(m.actionErr, "bash") {
		t.Fatalf("refusal should name the pane command, got %q", m.actionErr)
	}
}

// x arms a kill for the selected session; the footer prompts and nothing
// is killed yet.
func TestKillArmsWithFooterPrompt(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f) // alpha selected
	next, cmd := m.Update(keyRunes("x"))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("arming must not issue a command")
	}
	if m.confirmKill != "alpha" {
		t.Fatalf("confirmKill = %q, want alpha", m.confirmKill)
	}
	if v := m.View(); !strings.Contains(v, "kill alpha?") {
		t.Fatalf("footer should prompt for confirmation:\n%s", v)
	}
	if len(f.killedSessions) != 0 {
		t.Fatalf("nothing should be killed yet, got %v", f.killedSessions)
	}
}

// y confirms: the session is killed, its rows drop from the list without
// waiting for the next poll, and the cursor moves to the next row.
func TestKillConfirmKillsAndReselects(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f) // alpha selected
	next, _ := m.Update(keyRunes("x"))
	m = next.(Model)
	next, cmd := m.Update(keyRunes("y"))
	m = next.(Model)
	if m.confirmKill != "" {
		t.Fatalf("confirm should disarm, got %q", m.confirmKill)
	}
	if m.indexOf("%1") >= 0 {
		t.Fatal("killed session's rows should drop immediately")
	}
	if m.selectedID != "%2" {
		t.Fatalf("cursor should move to the next row, got %q", m.selectedID)
	}
	m = drive(t, m, cmd)
	if len(f.killedSessions) != 1 || f.killedSessions[0] != "alpha" {
		t.Fatalf("killedSessions = %v, want [alpha]", f.killedSessions)
	}
	if m.actionErr != "" {
		t.Fatalf("successful kill should not set actionErr, got %q", m.actionErr)
	}
}

// q arms a quit confirm instead of exiting — it sits next to x on the
// keyboard, so a slip must not drop the whole hub.
func TestQuitArmsWithFooterPrompt(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f)
	next, cmd := m.Update(keyRunes("q"))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("arming must not issue a command")
	}
	if !m.confirmQuit {
		t.Fatal("q should arm confirmQuit")
	}
	if v := m.View(); !strings.Contains(v, "quit coop?") {
		t.Fatalf("footer should prompt for confirmation:\n%s", v)
	}
}

func TestQuitConfirmQuits(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f)
	next, _ := m.Update(keyRunes("q"))
	m = next.(Model)
	next, cmd := m.Update(keyRunes("y"))
	m = next.(Model)
	if m.confirmQuit {
		t.Fatal("confirm should disarm")
	}
	if cmd == nil {
		t.Fatal("y should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("y should produce tea.Quit, got %T", cmd())
	}
}

// Any key besides y disarms and is swallowed — no surprise navigation.
func TestQuitOtherKeyDisarmsAndIsSwallowed(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f) // alpha (%1) selected
	next, _ := m.Update(keyRunes("q"))
	m = next.(Model)
	next, cmd := m.Update(keyRunes("j"))
	m = next.(Model)
	if m.confirmQuit || cmd != nil {
		t.Fatalf("other key should disarm without a command (confirmQuit=%v)", m.confirmQuit)
	}
	if m.selectedID != "%1" {
		t.Fatalf("swallowed key must not move the cursor, got %q", m.selectedID)
	}
}

// k in the quit prompt kills every managed session and quits — with no
// session mid-task there is no extra confirm.
func TestQuitKillAllKillsAndQuits(t *testing.T) {
	f := &fakeTmux{panes: testPanes()} // alpha idle, beta needs input
	m := pollOnce(t, f)
	next, _ := m.Update(keyRunes("q"))
	m = next.(Model)
	next, cmd := m.Update(keyRunes("k"))
	m = next.(Model)
	if m.confirmQuit || m.confirmKillAll != 0 {
		t.Fatalf("no session is working, so k should not re-arm (quit=%v killAll=%d)",
			m.confirmQuit, m.confirmKillAll)
	}
	if cmd == nil {
		t.Fatal("k should produce a kill-all command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("kill-all should end in tea.QuitMsg, got %T", cmd())
	}
	slices.Sort(f.killedSessions)
	if !slices.Equal(f.killedSessions, []string{"alpha", "beta"}) {
		t.Fatalf("killedSessions = %v, want [alpha beta] (never the hub)", f.killedSessions)
	}
}

// With sessions mid-task, k detours through a second confirm showing the
// working count; y then proceeds.
func TestQuitKillAllWarnsWhenWorking(t *testing.T) {
	panes := testPanes()
	panes[1].Title = "⠂ refactoring…" // alpha working
	f := &fakeTmux{panes: panes}
	m := pollOnce(t, f)
	next, _ := m.Update(keyRunes("q"))
	m = next.(Model)
	next, cmd := m.Update(keyRunes("k"))
	m = next.(Model)
	if cmd != nil || len(f.killedSessions) != 0 {
		t.Fatalf("k should only arm the warning, killed %v", f.killedSessions)
	}
	if m.confirmKillAll != 1 {
		t.Fatalf("confirmKillAll = %d, want 1", m.confirmKillAll)
	}
	if v := m.View(); !strings.Contains(v, "1 session still working") {
		t.Fatalf("footer should warn about the working session:\n%s", v)
	}
	next, cmd = m.Update(keyRunes("y"))
	m = next.(Model)
	if m.confirmKillAll != 0 {
		t.Fatal("y should disarm the warning")
	}
	if cmd == nil {
		t.Fatal("y should produce the kill-all command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("kill-all should end in tea.QuitMsg, got %T", cmd())
	}
	slices.Sort(f.killedSessions)
	if !slices.Equal(f.killedSessions, []string{"alpha", "beta"}) {
		t.Fatalf("killedSessions = %v, want [alpha beta]", f.killedSessions)
	}
}

// Any key besides y at the working-sessions warning cancels the whole
// quit and is swallowed — nothing dies.
func TestQuitKillAllWarnOtherKeyCancels(t *testing.T) {
	panes := testPanes()
	panes[1].Title = "⠂ refactoring…"
	f := &fakeTmux{panes: panes}
	m := pollOnce(t, f) // alpha (%1) selected
	next, _ := m.Update(keyRunes("q"))
	m = next.(Model)
	next, _ = m.Update(keyRunes("k"))
	m = next.(Model)
	next, cmd := m.Update(keyRunes("j"))
	m = next.(Model)
	if m.confirmKillAll != 0 || cmd != nil {
		t.Fatalf("other key should disarm without a command (confirmKillAll=%d)", m.confirmKillAll)
	}
	if len(f.killedSessions) != 0 {
		t.Fatalf("nothing should be killed, got %v", f.killedSessions)
	}
	if m.selectedID != "%1" {
		t.Fatalf("swallowed key must not move the cursor, got %q", m.selectedID)
	}
}

func TestKillEscCancels(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f)
	next, _ := m.Update(keyRunes("x"))
	m = next.(Model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.confirmKill != "" || cmd != nil {
		t.Fatalf("esc should disarm without a command (confirmKill=%q)", m.confirmKill)
	}
	if len(f.killedSessions) != 0 {
		t.Fatalf("nothing should be killed, got %v", f.killedSessions)
	}
}

// Any key besides y confirms nothing: it disarms and is swallowed — no
// surprise navigation while the prompt is up.
func TestKillOtherKeyDisarmsAndIsSwallowed(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f) // alpha (%1) selected
	next, _ := m.Update(keyRunes("x"))
	m = next.(Model)
	next, cmd := m.Update(keyRunes("j"))
	m = next.(Model)
	if m.confirmKill != "" || cmd != nil {
		t.Fatalf("other key should disarm without a command (confirmKill=%q)", m.confirmKill)
	}
	if m.selectedID != "%1" {
		t.Fatalf("swallowed key must not move the cursor, got %q", m.selectedID)
	}
	if len(f.killedSessions) != 0 {
		t.Fatalf("nothing should be killed, got %v", f.killedSessions)
	}
}

func TestKillErrorSurfacesInFooter(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f)
	next, _ := m.Update(keyRunes("x"))
	m = next.(Model)
	f.err = errFake
	next, cmd := m.Update(keyRunes("y"))
	m = next.(Model)
	m = drive(t, m, cmd)
	if !strings.Contains(m.actionErr, "kill") {
		t.Fatalf("kill error should surface in actionErr, got %q", m.actionErr)
	}
}

// The armed session dying on its own (poll no longer lists it) disarms
// the prompt silently.
func TestKillDisarmsWhenArmedSessionVanishes(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := pollOnce(t, f) // alpha selected
	next, _ := m.Update(keyRunes("x"))
	m = next.(Model)
	f.panes = []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "beta", ID: "%2", Title: "🔔 permission needed", Cmd: "claude", Path: "/repos/beta-repo"},
	}
	m = drive(t, m, m.poll())
	if m.confirmKill != "" {
		t.Fatalf("vanished session should disarm, got %q", m.confirmKill)
	}
}

func TestKillWithNoSessionsIsInert(t *testing.T) {
	m := pollOnce(t, &fakeTmux{})
	next, cmd := m.Update(keyRunes("x"))
	m = next.(Model)
	if m.confirmKill != "" || cmd != nil {
		t.Fatalf("x with no sessions must not arm (confirmKill=%q)", m.confirmKill)
	}
}

func TestLivePaneCreatedWhenMissing(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	if m.livePane != "%50" {
		t.Fatalf("live pane id not recorded, got %q", m.livePane)
	}
	if m.LivePane() != "%50" {
		t.Fatal("LivePane accessor must expose the pane id (main's quit cleanup uses it)")
	}
	if len(f.splits) != 1 || f.splits[0] != "roost" {
		t.Fatalf("expected one split of the hub session, got %v", f.splits)
	}
	if f.paneOpts["%50/"+hub.LiveMarker] != "1" {
		t.Fatal("live pane must be tagged with the marker option")
	}
	if f.paneOpts["%50/remain-on-exit"] != "on" {
		t.Fatal("live pane must set remain-on-exit for respawnability")
	}
}

func TestLivePaneAdoptedNotDuplicated(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), marked: "%50"}
	m := bootLive(t, f)
	if m.livePane != "%50" {
		t.Fatalf("marked pane should be adopted, got %q", m.livePane)
	}
	if len(f.splits) != 0 {
		t.Fatalf("adoption must not split again, got %v", f.splits)
	}
	if f.paneOpts["%50/remain-on-exit"] != "on" {
		t.Fatal("adoption must re-assert remain-on-exit in case it was never set")
	}
}

// allow-set-title only exists from tmux 3.5 on. An older server rejects
// it, and that must not cost us the pane we just made: dropping the id
// left livePane empty, so the next poll split another one, and another,
// one per tick until tmux ran out of width.
func TestLivePaneSurvivesUnknownOption(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50",
		unknownOpts: []string{"allow-set-title"}}
	m := bootLive(t, f)
	if m.livePane != "%50" {
		t.Fatalf("live pane id must survive an unknown option, got %q", m.livePane)
	}
	// Second poll: the pane is known, so nothing splits again.
	next, _ := m.Update(m.pollMsgNow(t))
	m = next.(Model)
	if len(f.splits) != 1 {
		t.Fatalf("expected exactly one split, got %v", f.splits)
	}
	if m.errMsg != "" {
		t.Fatalf("a missing optional option is not a user-visible error, got %q", m.errMsg)
	}
}

// Defence in depth for the same failure mode: whatever goes wrong after
// the split, a pane that exists is adopted rather than orphaned and
// re-split on the next tick.
func TestLivePaneAdoptedDespiteError(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	m.livePane, m.liveTarget = "", ""
	next, _ := m.Update(livePaneMsg{id: "%51", err: errors.New("boom")})
	m = next.(Model)
	if m.livePane != "%51" {
		t.Fatalf("live pane id must be kept alongside the error, got %q", m.livePane)
	}
	if m.errMsg == "" {
		t.Fatal("the error should still surface in the footer")
	}
}

func TestLivePaneRetargetsToSelection(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f) // alpha (top of the list) is selected
	if len(f.respawns) != 1 {
		t.Fatalf("expected one respawn after boot, got %v", f.respawns)
	}
	if r := f.respawns[0]; r[0] != "%50" || !strings.Contains(r[1], "alpha") {
		t.Fatalf("live pane should attach alpha, got %v", r)
	}

	// Selection change retargets immediately.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown}) // → beta
	m = next.(Model)
	m, _ = driveCmd(t, m, cmd)
	if len(f.respawns) != 2 || !strings.Contains(f.respawns[1][1], "beta") {
		t.Fatalf("expected retarget to beta, got %v", f.respawns)
	}
}

func TestNoRetargetWhenSelectionUnchanged(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	_, cmd := m.Update(m.pollMsgNow(t)) // second poll, same selection
	if cmd != nil {
		t.Fatal("unchanged selection must not issue a command")
	}
	if len(f.respawns) != 1 {
		t.Fatalf("no extra respawn expected, got %v", f.respawns)
	}
}

func TestLivePaneShowsPlaceholderWhenSessionsGone(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	// All claude sessions vanish; hub + live pane remain.
	f.panes = []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "roost", ID: "%50", Title: "live", Cmd: "tmux", Hub: true},
	}
	m, cmd := driveCmd(t, m, m.poll())
	m, _ = driveCmd(t, m, cmd) // retargetMsg
	last := f.respawns[len(f.respawns)-1]
	if !strings.Contains(last[1], "no session selected") {
		t.Fatalf("expected placeholder respawn, got %v", last)
	}
}

func TestLivePaneRecreatedWhenKilled(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	// Someone kills the live pane: it disappears from list-panes.
	f.panes = testPanes()
	m, cmd := driveCmd(t, m, m.poll()) // liveGone → ensure again
	m, _ = driveCmd(t, m, cmd)         // livePaneMsg
	if len(f.splits) != 2 {
		t.Fatalf("live pane should be recreated, splits=%v", f.splits)
	}
	if m.livePane != "%50" {
		t.Fatalf("recreated pane id not recorded, got %q", m.livePane)
	}
}

// Retargeting the preview also retitles its tmux title bar with the new
// session's status, age, and task text. No session name: the nav list
// groups by repo and doesn't show names either.
func TestRetargetSetsPreviewTitle(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f) // alpha (top of the list) selected
	if len(f.titles) != 1 || f.titles[0] != [2]string{"%50", "idle · - · Claude Code"} {
		t.Fatalf("titles = %v, want alpha's title set on %%50", f.titles)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown}) // → beta
	m = next.(Model)
	m, _ = driveCmd(t, m, cmd)
	if len(f.titles) != 2 || f.titles[1] != [2]string{"%50", "NEEDS INPUT · - · permission needed"} {
		t.Fatalf("titles = %v, want beta's title appended", f.titles)
	}
}

// The poll keeps the title bar fresh as status/age drift, and skips the
// call when nothing changed.
func TestPollRefreshesPreviewTitle(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	m, _ = driveCmd(t, m, m.poll()) // same status → no retitle
	if len(f.titles) != 1 {
		t.Fatalf("unchanged status must not retitle, got %v", f.titles)
	}
	// alpha (selected) starts working: idle → working.
	for i := range f.panes {
		if f.panes[i].Session == "alpha" {
			f.panes[i].Title = "⠂ compiling"
		}
	}
	m = drive(t, m, m.poll())
	if len(f.titles) != 2 || f.titles[1] != [2]string{"%50", "working · - · compiling"} {
		t.Fatalf("titles = %v, want refreshed alpha title", f.titles)
	}
	m, _ = driveCmd(t, m, m.poll()) // and it dedupes again
	if len(f.titles) != 2 {
		t.Fatalf("second identical poll must not retitle, got %v", f.titles)
	}
}

// With every session gone the preview shows the placeholder — the title
// bar must say so instead of naming a dead session.
func TestPlaceholderPreviewTitle(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	m := bootLive(t, f)
	f.panes = []hub.Pane{
		{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
		{Session: "roost", ID: "%50", Title: "live", Cmd: "tmux", Hub: true},
	}
	m, cmd := driveCmd(t, m, m.poll())
	m, _ = driveCmd(t, m, cmd) // retargetMsg
	last := f.titles[len(f.titles)-1]
	if last != [2]string{"%50", "no session"} {
		t.Fatalf("titles = %v, want trailing 'no session'", f.titles)
	}
}

// The live pane's border bar renders just the title — tmux's default
// format adds a pane index and quotes — and pins the text to a readable
// foreground so it doesn't dim with the border when the preview is
// unfocused.
func TestLivePaneTitleBarFormat(t *testing.T) {
	want := " #[fg=colour250]#{pane_title}#[default] "
	f := &fakeTmux{panes: livePanes(), splitID: "%50"}
	bootLive(t, f)
	if got := f.paneOpts["%50/pane-border-format"]; got != want {
		t.Fatalf("pane-border-format = %q, want %q", got, want)
	}
	// Adoption re-asserts it too (prior run may predate the option).
	f2 := &fakeTmux{panes: livePanes(), marked: "%50"}
	bootLive(t, f2)
	if got := f2.paneOpts["%50/pane-border-format"]; got != want {
		t.Fatalf("adopted pane-border-format = %q, want %q", got, want)
	}
}

// The bar appends the session's task text (its cleaned pane title);
// a title that cleans to nothing drops the segment and its separator.
func TestLiveTitleForTaskText(t *testing.T) {
	panes := []hub.Pane{
		{Session: "alpha", ID: "%1", Title: "⠂ compiling", Status: hub.StatusWorking},
		{Session: "beta", ID: "%2", Title: "✳ ", Status: hub.StatusIdle},
	}
	if got := liveTitleFor(panes, "%1"); got != "working · - · compiling" {
		t.Fatalf("liveTitleFor(%%1) = %q", got)
	}
	if got := liveTitleFor(panes, "%2"); got != "idle · -" {
		t.Fatalf("liveTitleFor(%%2) = %q, want no trailing separator", got)
	}
	if got := liveTitleFor(panes, "%gone"); got != "no session" {
		t.Fatalf("liveTitleFor(%%gone) = %q", got)
	}
}

// newPicker builds a model with a canned repo loader and one poll done.
func newPicker(t *testing.T, f *fakeTmux, repos []string) Model {
	t.Helper()
	m := New(f, []string{"claude", "node"}, "roost", "cc", "", "claude",
		func() ([]string, error) { return repos, nil }, nil, 0, ArbiterConfig{})
	return drive(t, m, m.poll())
}

func TestPickerOpenNavigateCancel(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/gamma", "/tmp/delta"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	if !m.picking || m.repoIdx != 0 {
		t.Fatalf("picking=%v repoIdx=%d after n", m.picking, m.repoIdx)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.repoIdx != 1 {
		t.Fatalf("repoIdx = %d after down, want 1", m.repoIdx)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.picking || len(f.created) != 0 {
		t.Fatalf("esc must cancel without creating: picking=%v created=%v",
			m.picking, f.created)
	}
}

func TestPickerTypeToFilter(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/alpha", "/tmp/beta", "/other/gamma"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	// Move the cursor first — typing must snap it back to the top so it
	// never points at a hidden entry.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	next, _ = m.Update(keyRunes("g"))
	m = next.(Model)
	if got := m.filteredRepos(); len(got) != 1 || got[0] != "/other/gamma" {
		t.Fatalf("filteredRepos = %v, want [/other/gamma]", got)
	}
	if m.repoIdx != 0 {
		t.Fatalf("repoIdx = %d after typing, want 0", m.repoIdx)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drive(t, next.(Model), cmd)
	if len(f.created) != 1 || f.created[0][1] != "/other/gamma" {
		t.Fatalf("created = %v, want dir /other/gamma", f.created)
	}
}

func TestPickerFilterIsCaseInsensitiveOnBaseName(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/Alpha", "/gamma/beta"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, _ = m.Update(keyRunes("AL"))
	m = next.(Model)
	if got := m.filteredRepos(); len(got) != 1 || got[0] != "/tmp/Alpha" {
		t.Fatalf("filteredRepos = %v, want [/tmp/Alpha]", got)
	}
	// "gamma" only appears in beta's parent dir — the filter must not
	// match path segments above the base name.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	next, _ = m.Update(keyRunes("gamma"))
	m = next.(Model)
	if got := m.filteredRepos(); len(got) != 0 {
		t.Fatalf("filteredRepos = %v, want none (dir names don't match)", got)
	}
}

func TestPickerFilterBackspaceEscAndEmptyEnter(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/alpha", "/tmp/beta"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, _ = m.Update(keyRunes("zz"))
	m = next.(Model)
	if got := m.filteredRepos(); len(got) != 0 {
		t.Fatalf("filteredRepos = %v, want none", got)
	}
	// With no matches the add row is all that is left to select, so
	// enter opens the add prompt rather than creating anything. Esc
	// backs out of it, leaving the filter as it was.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.adding || cmd != nil || len(f.created) != 0 {
		t.Fatalf("enter with no matches should open the add prompt: adding=%v created=%v",
			m.adding, f.created)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.adding || !m.picking || m.repoFilter != "zz" {
		t.Fatalf("esc should leave the prompt for the picker: adding=%v picking=%v filter=%q",
			m.adding, m.picking, m.repoFilter)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.repoFilter != "z" {
		t.Fatalf("repoFilter = %q after backspace, want z", m.repoFilter)
	}
	// Esc is two-stage: clear the filter first, close only when empty.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if !m.picking || m.repoFilter != "" {
		t.Fatalf("first esc should clear filter, keep picker: picking=%v filter=%q",
			m.picking, m.repoFilter)
	}
	if got := m.filteredRepos(); len(got) != 2 {
		t.Fatalf("filteredRepos = %v, want full list back", got)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.picking {
		t.Fatal("second esc should close the picker")
	}
}

func TestPickerSortsReposByBaseName(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/gamma", "/other/delta", "/tmp/alpha"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	want := []string{"/tmp/alpha", "/other/delta", "/tmp/gamma"}
	for i := range want {
		if m.repos[i] != want[i] {
			t.Fatalf("repos = %v, want %v", m.repos, want)
		}
	}
}

func TestPickerCreateSelectsNewSession(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/gamma"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.picking {
		t.Fatal("enter must close the picker")
	}
	m = drive(t, m, cmd) // run createCmd → createdMsg
	want := [3]string{"gamma", "/tmp/gamma", "claude"}
	if len(f.created) != 1 || f.created[0] != want {
		t.Fatalf("created = %v, want %v", f.created, want)
	}
	f.panes = append(f.panes, hub.Pane{Session: "gamma", ID: "%7",
		Title: "✳ Claude Code", Cmd: "claude"})
	m = drive(t, m, m.poll())
	if m.selectedID != "%7" {
		t.Fatalf("selectedID = %q, want %%7 (new session auto-selected)",
			m.selectedID)
	}
	if m.pendingSession != "" {
		t.Fatalf("pendingSession = %q, want cleared", m.pendingSession)
	}
}

func TestPickerDedupsSessionName(t *testing.T) {
	f := &fakeTmux{panes: testPanes()} // includes session "alpha"
	m := newPicker(t, f, []string{"/repos/alpha"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drive(t, next.(Model), cmd)
	if len(f.created) != 1 || f.created[0][0] != "alpha-2" {
		t.Fatalf("created = %v, want name alpha-2", f.created)
	}
}

func TestPickerRapidDoubleCreateDedups(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/gamma"})
	for i := 0; i < 2; i++ { // two creates before any poll shows the first
		next, _ := m.Update(keyRunes("n"))
		m = next.(Model)
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drive(t, next.(Model), cmd)
	}
	if len(f.created) != 2 || f.created[0][0] != "gamma" || f.created[1][0] != "gamma-2" {
		t.Fatalf("created = %v, want gamma then gamma-2", f.created)
	}
}

func TestPickerCreateSizesToLivePane(t *testing.T) {
	f := &fakeTmux{panes: testPanes(), paneW: 120, paneH: 40}
	m := newPicker(t, f, []string{"/tmp/gamma"})
	m.livePane = "%50"

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	drive(t, next.(Model), cmd)
	if len(f.createdSizes) != 1 || f.createdSizes[0] != [2]int{120, 40} {
		t.Fatalf("createdSizes = %v, want [[120 40]]", f.createdSizes)
	}

	// No live pane yet → 0,0 lets tmux use its default.
	f2 := &fakeTmux{panes: testPanes(), paneW: 120, paneH: 40}
	m2 := newPicker(t, f2, []string{"/tmp/gamma"})
	next, _ = m2.Update(keyRunes("n"))
	m2 = next.(Model)
	next, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	drive(t, next.(Model), cmd)
	if len(f2.createdSizes) != 1 || f2.createdSizes[0] != [2]int{0, 0} {
		t.Fatalf("createdSizes = %v, want [[0 0]] without a live pane", f2.createdSizes)
	}
}

func TestCreateTriggersImmediatePoll(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/gamma"})

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	m, cmd = driveCmd(t, m, cmd) // createdMsg → immediate poll
	if cmd == nil {
		t.Fatal("createdMsg should chain into an immediate poll")
	}
	msg := cmd()
	if _, ok := msg.(pollMsg); !ok {
		t.Fatalf("createdMsg should chain into an immediate poll, got %T", msg)
	}
}

// createGamma runs the picker create flow on a booted-live model and
// returns it with the createdMsg handled — cmd is the immediate poll.
func createGamma(t *testing.T, f *fakeTmux, m Model) (Model, tea.Cmd) {
	t.Helper()
	m.loadRepos = func() ([]string, error) { return []string{"/tmp/gamma"}, nil }
	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	m, cmd = driveCmd(t, m, cmd) // createCmd → createdMsg → poll
	f.panes = append(livePanes(), hub.Pane{Session: "gamma", ID: "%7",
		Title: "✳ Claude Code", Cmd: "claude"})
	return m, cmd
}

func TestCreateAutoFocusesPreview(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), marked: "%50"}
	m, cmd := createGamma(t, f, bootLive(t, f))

	m, cmd = driveCmd(t, m, cmd) // pollMsg → retarget
	if m.selectedID != "%7" {
		t.Fatalf("selectedID = %q, want %%7", m.selectedID)
	}
	m, cmd = driveCmd(t, m, cmd) // retargetMsg → resizeLive
	m, cmd = driveCmd(t, m, cmd) // resizedMsg → focus
	if cmd == nil {
		t.Fatal("expected a focus command once the preview shows the new session")
	}
	m = drive(t, m, cmd) // focusedMsg
	if !slices.Contains(f.selected, "%50") {
		t.Fatalf("expected focus on live pane %%50, got %v", f.selected)
	}
	if m.actionErr != "" {
		t.Fatalf("auto-focus should not set actionErr, got %q", m.actionErr)
	}
}

func TestKeypressCancelsCreateAutoFocus(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), marked: "%50"}
	m, cmd := createGamma(t, f, bootLive(t, f))

	m, cmd = driveCmd(t, m, cmd)       // pollMsg → retarget
	next, _ := m.Update(keyRunes("z")) // user typed before the preview settled
	m = next.(Model)
	m, cmd = driveCmd(t, m, cmd) // retargetMsg → resizeLive
	next, cmd = m.Update(cmd())  // resizedMsg — focus must not fire
	if cmd != nil {
		t.Fatal("keypress before the preview settles should cancel the auto-focus")
	}
	if len(f.selected) != 0 {
		t.Fatalf("no pane should be focused, got %v", f.selected)
	}
}

func TestPickerConfigErrors(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := New(f, []string{"claude"}, "roost", "cc", "", "claude",
		func() ([]string, error) { return nil, fmt.Errorf("boom: no config") },
		nil, 0, ArbiterConfig{})
	m = drive(t, m, m.poll())
	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	if m.picking || !strings.Contains(m.actionErr, "boom") {
		t.Fatalf("picking=%v actionErr=%q", m.picking, m.actionErr)
	}

	// An empty list still opens: its add row is the only way to
	// configure a first repo, so refusing would be a dead end.
	empty := newPicker(t, &fakeTmux{panes: testPanes()}, nil)
	next, _ = empty.Update(keyRunes("n"))
	empty = next.(Model)
	if !empty.picking || empty.actionErr != "" {
		t.Fatalf("empty repos must open the picker: picking=%v err=%q",
			empty.picking, empty.actionErr)
	}
	if empty.repoIdx != 0 || !strings.Contains(empty.View(), "+ add new repo") {
		t.Fatalf("add row should be selected and drawn:\n%s", empty.View())
	}
}

// pickerAdd is newPicker with a stubbed config writer, recording the
// paths the add row hands it.
func pickerAdd(t *testing.T, f *fakeTmux, repos []string, dir string,
	err error) (Model, *[]string) {
	t.Helper()
	m := newPicker(t, f, repos)
	var asked []string
	m.addRepo = func(repo string) (string, error) {
		asked = append(asked, repo)
		return dir, err
	}
	return m, &asked
}

func TestPickerAddRowAddsRepoAndStartsSession(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m, asked := pickerAdd(t, f, []string{"/repos/gamma"}, "/repos/sprocket-v2", nil)

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	// One row past the last repo is the add row, and down stops there.
	for range 2 {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(Model)
	}
	if m.repoIdx != 1 {
		t.Fatalf("repoIdx = %d, want 1 (the add row, and no further)", m.repoIdx)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.adding || cmd != nil {
		t.Fatalf("enter on the add row should open the prompt: adding=%v", m.adding)
	}
	// An empty path is inert — nothing to save.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter on an empty path should be inert")
	}
	next, _ = m.Update(keyRunes("~/src/sprocket-v2"))
	m = next.(Model)
	if m.repoPath != "~/src/sprocket-v2" {
		t.Fatalf("repoPath = %q", m.repoPath)
	}
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd = driveCmd(t, next.(Model), cmd) // addRepoCmd → repoAddedMsg
	if m.picking || m.adding || m.repoPath != "" {
		t.Fatalf("a saved repo should close the picker: picking=%v adding=%v path=%q",
			m.picking, m.adding, m.repoPath)
	}
	if len(*asked) != 1 || (*asked)[0] != "~/src/sprocket-v2" {
		t.Fatalf("addRepo asked %v, want the path as typed", *asked)
	}
	m = drive(t, m, cmd) // createCmd → createdMsg
	want := [3]string{"sprocket-v2", "/repos/sprocket-v2", "claude"}
	if len(f.created) != 1 || f.created[0] != want {
		t.Fatalf("created = %v, want %v", f.created, want)
	}
	if m.pendingSession != "sprocket-v2" {
		t.Fatalf("pendingSession = %q, want the new session", m.pendingSession)
	}
}

func TestPickerAddRowRejectedPathKeepsPrompt(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m, _ := pickerAdd(t, f, nil, "", fmt.Errorf("stat /repos/nop: no such file"))

	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // add row: the only row
	m = next.(Model)
	next, _ = m.Update(keyRunes("/repos/nope"))
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.repoPath != "/repos/nop" {
		t.Fatalf("repoPath = %q after backspace, want /repos/nop", m.repoPath)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drive(t, next.(Model), cmd)
	if !m.picking || !m.adding || m.repoPath != "/repos/nop" {
		t.Fatalf("a rejected path must leave the prompt and text up: "+
			"picking=%v adding=%v path=%q", m.picking, m.adding, m.repoPath)
	}
	if !strings.Contains(m.actionErr, "no such file") {
		t.Fatalf("actionErr = %q, want the writer's error", m.actionErr)
	}
	if len(f.created) != 0 {
		t.Fatalf("created = %v, want nothing", f.created)
	}
}

func TestPickerClickSelectsRepoAndAddRow(t *testing.T) {
	click := func(y int) tea.MouseMsg {
		return tea.MouseMsg{X: 2, Y: y,
			Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	}
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/repos/alpha", "/repos/beta"})
	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	// Screen rows: border(0), prompt(1), blank(2), alpha(3), its path(4),
	// beta(5), its path(6), blank(7), add row(8).
	next, _ = m.Update(click(5))
	m = next.(Model)
	if m.repoIdx != 1 {
		t.Fatalf("repoIdx = %d after clicking beta, want 1", m.repoIdx)
	}
	next, _ = m.Update(click(8))
	m = next.(Model)
	if m.repoIdx != 2 {
		t.Fatalf("repoIdx = %d after clicking the add row, want 2", m.repoIdx)
	}
	// A click only moves the cursor; enter is what acts.
	if m.adding || len(f.created) != 0 {
		t.Fatalf("click must not act: adding=%v created=%v", m.adding, f.created)
	}
	next, _ = m.Update(click(2)) // the blank under the prompt
	m = next.(Model)
	if m.repoIdx != 2 {
		t.Fatalf("repoIdx = %d after clicking a blank row, want it unmoved", m.repoIdx)
	}
}

func TestPickerLongListKeepsAddRowInFrame(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	var repos []string
	for i := range 8 {
		repos = append(repos, fmt.Sprintf("/repos/repo-%d", i))
	}
	m := newPicker(t, f, repos)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = next.(Model)
	next, _ = m.Update(keyRunes("n"))
	m = next.(Model)
	// Walk to the add row: eight repos, then one row more.
	for range 8 {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(Model)
	}
	if m.repoIdx != 8 {
		t.Fatalf("repoIdx = %d, want 8 (the add row)", m.repoIdx)
	}
	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines != 12 {
		t.Fatalf("view is %d lines, want 12", lines)
	}
	// The list scrolls under the add row rather than pushing it out.
	if !strings.Contains(view, "+ add new repo") {
		t.Fatalf("add row fell out of the frame:\n%s", view)
	}
	if !strings.Contains(view, "repo-7") || strings.Contains(view, "repo-0") {
		t.Fatalf("list should be windowed onto the tail:\n%s", view)
	}
	// What is drawn is what a click hits.
	if got := m.pickerRowAt(8); got != 8 {
		t.Fatalf("pickerRowAt(8) = %d, want the add row (8):\n%s", got, view)
	}
}

func TestWindowResizeResizesPreviewedSession(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", paneW: 100, paneH: 30}
	m := bootLive(t, f) // previewing alpha (top of the list)
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("window resize should issue a resize command")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if len(f.resizes) != 1 || f.resizes[0] != "alpha 100x30" {
		t.Fatalf("resizes = %v, want [alpha 100x30]", f.resizes)
	}
	if m.errMsg != "" {
		t.Fatalf("successful resize should not set errMsg, got %q", m.errMsg)
	}
}

func TestWindowResizeSkipsSessionWithPrimaryClient(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", paneW: 100, paneH: 30,
		primary: true}
	m := bootLive(t, f)
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("window resize should issue a resize command")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if len(f.resizes) != 0 {
		t.Fatalf("session with a primary client must not be resized, got %v", f.resizes)
	}
	if m.errMsg != "" {
		t.Fatalf("skipping resize should not set errMsg, got %q", m.errMsg)
	}
}

func TestWindowResizeWithoutLivePaneIsNoop(t *testing.T) {
	m := pollOnce(t, &fakeTmux{panes: testPanes()})
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	if cmd != nil {
		t.Fatal("resize with no live pane should issue no command")
	}
}

func TestRetargetResizesNewTarget(t *testing.T) {
	f := &fakeTmux{panes: livePanes(), splitID: "%50", paneW: 90, paneH: 25}
	m := bootLive(t, f)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown}) // → beta
	m = next.(Model)
	m, cmd = driveCmd(t, m, cmd) // retargetMsg → resize cmd
	m, _ = driveCmd(t, m, cmd)   // resizedMsg
	if len(f.resizes) != 1 || f.resizes[0] != "beta 90x25" {
		t.Fatalf("resizes = %v, want [beta 90x25]", f.resizes)
	}
}

func TestPickerOtherKeysInert(t *testing.T) {
	f := &fakeTmux{panes: testPanes()}
	m := newPicker(t, f, []string{"/tmp/gamma"})
	next, _ := m.Update(keyRunes("n"))
	m = next.(Model)
	for _, k := range []string{"q", "s", "1"} {
		next, cmd := m.Update(keyRunes(k))
		m = next.(Model)
		if !m.picking || cmd != nil {
			t.Fatalf("key %q must be inert while picking (picking=%v cmd=%v)",
				k, m.picking, cmd)
		}
	}
	if len(f.sent) != 0 {
		t.Fatal("no keys may reach panes while picking")
	}
}

// Done flows through the poll pipeline: a session seen working that
// falls idle shows done; selection alone (preview client) leaves it,
// and focusing the live pane on it clears it.
func TestPollShowsDoneAndPreviewFocusClears(t *testing.T) {
	pane := func(title string) []hub.Pane {
		return []hub.Pane{
			{Session: "roost", ID: "%9", Title: "coop", Cmd: "coop", Hub: true},
			{Session: "roost", ID: "%50", Title: "live", Cmd: "tmux", Hub: true},
			{Session: "alpha", ID: "%1", Title: title, Cmd: "claude", Path: "/repos/alpha-repo"},
		}
	}
	f := &fakeTmux{panes: pane("⠂ compiling")}
	m := New(f, []string{"claude", "node"}, "roost", "cc", "", "claude", nil, nil,
		5*time.Minute, ArbiterConfig{})
	m = drive(t, m, m.poll())

	f.panes = pane("✳ Claude Code")
	m = drive(t, m, m.poll())
	if m.panes[0].Status != hub.StatusDone {
		t.Fatalf("finished session should show done, got %v", m.panes[0].Status)
	}

	// Selected with the preview attached but nav still focused: stays done.
	m.livePane, m.liveTarget = "%50", "alpha"
	f.activePane = "%0" // nav pane holds focus
	m = drive(t, m, m.poll())
	if m.panes[0].Status != hub.StatusDone {
		t.Fatalf("selection alone must not clear done, got %v", m.panes[0].Status)
	}

	// Focus moves into the live pane showing alpha: done clears for good.
	f.activePane = "%50"
	m = drive(t, m, m.poll())
	if m.panes[0].Status != hub.StatusIdle {
		t.Fatalf("focusing the preview should clear done, got %v", m.panes[0].Status)
	}
	f.activePane = "%0"
	m = drive(t, m, m.poll())
	if m.panes[0].Status != hub.StatusIdle {
		t.Fatalf("done must stay cleared after the visit, got %v", m.panes[0].Status)
	}
}

// A directly attached primary client also counts as a visit.
func TestPollPrimaryClientClearsDone(t *testing.T) {
	pane := func(title string) []hub.Pane {
		return []hub.Pane{
			{Session: "alpha", ID: "%1", Title: title, Cmd: "claude", Path: "/repos/alpha-repo"},
		}
	}
	f := &fakeTmux{panes: pane("⠂ compiling")}
	m := New(f, []string{"claude", "node"}, "roost", "cc", "", "claude", nil, nil,
		5*time.Minute, ArbiterConfig{})
	m = drive(t, m, m.poll())

	f.panes = pane("✳ Claude Code")
	f.primary = true
	m = drive(t, m, m.poll())
	if m.panes[0].Status != hub.StatusIdle {
		t.Fatalf("attached session must not show done, got %v", m.panes[0].Status)
	}
}

func TestArbiterKeyCycle(t *testing.T) {
	f := &fakeTmux{}
	m := New(f, []string{"claude"}, "roost", "cc", "", "claude", nil, nil, 0,
		ArbiterConfig{Model: "sonnet", ConfigDir: t.TempDir()})

	// No arbiter session: a launches one (recommend by construction).
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("a with no arbiter returned no command")
	}
	if msg, ok := cmd().(createdMsg); !ok || msg.err != nil {
		t.Fatalf("launch = %#v", msg)
	}
	if len(f.created) != 1 || f.created[0][0] != hub.ArbiterSession {
		t.Fatalf("created = %v", f.created)
	}
	// The hub's real allowlist rides into the session's env (M3 plumbing).
	if !strings.Contains(f.created[0][2], "COOP_ALLOWED_CMDS='claude'") {
		t.Errorf("arbiter cmd = %q, want the hub's allowed-cmds in its env", f.created[0][2])
	}

	// Arbiter in recommend: a flips the mode option to full.
	m.panes = []hub.Pane{{Session: "arbiter", ID: "%9", Arbiter: true,
		ArbiterMode: "recommend"}}
	mm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("a in recommend returned no command")
	}
	cmd()
	if f.sessionOpts["arbiter/"+hub.ArbiterModeMarker] != hub.ArbiterModeFull {
		t.Errorf("mode option = %q", f.sessionOpts["arbiter/"+hub.ArbiterModeMarker])
	}

	// Arbiter in full: a arms the kill confirm (y/esc like x).
	m.panes[0].ArbiterMode = "full"
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mm.(Model)
	if m.confirmKill != "arbiter" {
		t.Errorf("confirmKill = %q, want arbiter", m.confirmKill)
	}
}

func TestArbiterFooterDetail(t *testing.T) {
	m := New(&fakeTmux{}, nil, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.panes = []hub.Pane{{Session: "alpha", ID: "%1",
		Status: hub.StatusNeedsInput, ArbiterNote: "asking to run tests — suggest 1"}}
	m.selectedID = "%1"
	if foot := m.viewFooter(); !strings.Contains(foot, "asking to run tests") {
		t.Errorf("footer = %q, want the note", foot)
	}
	m.panes[0].ArbiterNote = ""
	m.panes[0].ArbiterLast = "1|1700000100|approved tests"
	if foot := m.viewFooter(); !strings.Contains(foot, "answered 1 by arbiter") {
		t.Errorf("footer = %q, want the answered line", foot)
	}
}

// space applies the arbiter's suggestion through the same gated send the
// digit keys use, and advertises itself only on a row that has one.
func TestSpaceAppliesArbiterSuggestion(t *testing.T) {
	f := &fakeTmux{cmd: "claude"}
	m := New(f, []string{"claude"}, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.panes = []hub.Pane{{Session: "alpha", ID: "%1", Status: hub.StatusNeedsInput,
		ArbiterNote: "asking to run tests", ArbiterSuggest: "2"}}
	m.selectedID = "%1"
	if foot := m.viewFooter(); !strings.Contains(foot, "space apply 2") {
		t.Errorf("footer = %q, want the apply chip", foot)
	}
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("space returned no command")
	}
	if msg, ok := cmd().(sentMsg); !ok || msg.err != nil {
		t.Fatalf("space sent %+v", msg)
	}
	if len(f.sent) != 1 || len(f.sent[0]) != 1 || f.sent[0][0] != "2" {
		t.Errorf("sent %v, want the suggested digit alone", f.sent)
	}

	// No suggestion on the row: no chip, no send, and the footer says why.
	m.panes[0].ArbiterSuggest = ""
	if foot := m.viewFooter(); strings.Contains(foot, "space apply") {
		t.Errorf("footer = %q, want no apply chip", foot)
	}
	mm, cmd = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("space with no suggestion returned a command")
	}
	if !strings.Contains(m.actionErr, "no arbiter suggestion") {
		t.Errorf("actionErr = %q", m.actionErr)
	}
}

func TestArbiterRowMarkAndHint(t *testing.T) {
	m := New(&fakeTmux{}, nil, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.panes = []hub.Pane{{Session: "alpha", ID: "%1",
		Status: hub.StatusNeedsInput, ArbiterNote: "note"}}
	m.selectedID = "%1"
	if nav := m.viewNav(); !strings.Contains(nav, "◆!") {
		t.Errorf("nav = %q, want the ! mark beside the status glyph", nav)
	}
	if foot := m.viewFooter(); !strings.Contains(foot, "a arbiter off") {
		t.Errorf("footer = %q, want the off hint", foot)
	}
	m.panes = append(m.panes, hub.Pane{Session: "arbiter", ID: "%9",
		Arbiter: true, ArbiterMode: "full"})
	if foot := m.viewFooter(); !strings.Contains(foot, "a arbiter full") {
		t.Errorf("footer = %q, want the full hint", foot)
	}
}

// The arbiter draws under its own divider with a fixed mode label:
// Claude's derived title names whatever it last triaged, which reads as
// a work session in that repo. Nor is it one of the watched sessions the
// header counts.
func TestArbiterPinnedRow(t *testing.T) {
	m := New(&fakeTmux{}, nil, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.panes = []hub.Pane{
		{Session: "alpha", ID: "%1", Path: "/home/user/sprocket-v2", Title: "✳ Claude Code"},
		{Session: "arbiter", ID: "%9", Path: "/home/user/.config/coop/arbiter",
			Arbiter: true, ArbiterMode: "full",
			Title: "✳ Sprocket v2 implementation review"},
	}
	m.selectedID = "%1"
	nav := m.viewNav()
	if !strings.Contains(nav, "─ arbiter ─") {
		t.Errorf("nav = %q, want the arbiter divider", nav)
	}
	if !strings.Contains(nav, "arbiter · full") {
		t.Errorf("nav = %q, want the fixed mode label", nav)
	}
	if strings.Contains(nav, "Sprocket v2 implementation") {
		t.Errorf("nav = %q, should not show the arbiter's derived title", nav)
	}
	if !strings.Contains(nav, "1 sessions") {
		t.Errorf("nav = %q, want the arbiter left out of the count", nav)
	}
}

// paneAt has to mirror the pinned section's two opening lines or every
// click below the divider lands on the wrong row.
func TestPaneAtWithPinnedArbiter(t *testing.T) {
	m := New(&fakeTmux{}, nil, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.width, m.height = navWidth, 24
	m.panes = []hub.Pane{
		{Session: "alpha", ID: "%1", Path: "/home/user/sprocket-v2", Title: "✳ Claude Code"},
		arbiterPane("%9", false),
	}
	m.selectedID = "%1"
	// border, title, blank, then: repo header, alpha, blank, divider, arbiter.
	for y, want := range map[int]string{3: "", 4: "%1", 5: "", 6: "", 7: "%9", 8: ""} {
		if got := m.paneAt(y); got != want {
			t.Errorf("paneAt(%d) = %q, want %q", y, got, want)
		}
	}
}

// x on the pinned row is the arbiter's off switch — the same y/esc
// confirm the a key's full → off arms, named so it reads plainly.
func TestKillArbiterRowConfirmsByName(t *testing.T) {
	m := New(&fakeTmux{}, nil, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.panes = []hub.Pane{
		{Session: "alpha", ID: "%1", Path: "/home/user/sprocket-v2"},
		arbiterPane("%9", false),
	}
	m.selectedID = "%9"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(Model)
	if m.confirmKill != "arbiter" {
		t.Fatalf("confirmKill = %q, want arbiter", m.confirmKill)
	}
	if foot := m.viewFooter(); !strings.Contains(foot, "kill arbiter?") {
		t.Errorf("footer = %q, want the named confirm", foot)
	}
}

func TestPollNudgesArbiter(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "alpha", ID: "%1", Title: "🔔 pick one"},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "recommend",
			ArbiterSeen: true},
	}}
	m := New(f, []string{"claude"}, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	if _, ok := m.poll()().(pollMsg); !ok {
		t.Fatal("poll did not return a pollMsg")
	}
	found := false
	for _, keys := range f.sent {
		if len(keys) == 2 && keys[0] == hub.NudgeText("alpha", "recommend") {
			found = true
		}
	}
	if !found {
		t.Errorf("no nudge sent; sent = %v", f.sent)
	}
}

// A just-launched arbiter isn't typed at until it has survived a poll —
// claude swallows the Enter on keys sent in its first half second.
func TestPollHoldsNudgeUntilArbiterSettles(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "alpha", ID: "%1", Title: "🔔 pick one"},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "recommend"},
	}}
	m := New(f, []string{"claude"}, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	if _, ok := m.poll()().(pollMsg); !ok {
		t.Fatal("poll did not return a pollMsg")
	}
	for _, keys := range f.sent {
		if len(keys) == 2 && keys[0] == hub.NudgeText("alpha", "recommend") {
			t.Fatalf("nudged a just-launched arbiter: %v", f.sent)
		}
	}
}

func TestWrapMsg(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		width, max int
		offset     int
		wantLines  []string
		wantTotal  int
	}{
		{name: "fits on one line", text: "short note", width: 20, max: 4,
			wantLines: []string{"short note"}, wantTotal: 1},
		{name: "wraps on word boundaries", text: "one two three four", width: 8, max: 4,
			wantLines: []string{"one two", "three", "four"}, wantTotal: 3},
		{name: "keeps a hyphenated word whole", text: "not 2 (don't-ask-again grant)",
			width: 20, max: 4,
			wantLines: []string{"not 2", "(don't-ask-again", "grant)"}, wantTotal: 3},
		{name: "hard-breaks a word longer than the width",
			text: "aaaaaaaaaaaa", width: 5, max: 4,
			wantLines: []string{"aaaaa", "aaaaa", "aa"}, wantTotal: 3},
		{name: "windows at the offset", text: "one two three four", width: 8, max: 2,
			offset: 1, wantLines: []string{"three", "four"}, wantTotal: 3},
		{name: "clamps an offset past the end", text: "one two three four",
			width: 8, max: 2, offset: 99,
			wantLines: []string{"three", "four"}, wantTotal: 3},
		{name: "clamps a negative offset", text: "one two three four",
			width: 8, max: 2, offset: -3,
			wantLines: []string{"one two", "three"}, wantTotal: 3},
		{name: "exact fit is not overflow", text: "one two", width: 8, max: 1,
			wantLines: []string{"one two"}, wantTotal: 1},
		{name: "empty text", text: "", width: 8, max: 4,
			wantLines: []string{""}, wantTotal: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, total := wrapMsg(tt.text, tt.width, tt.max, tt.offset)
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
			if !slices.Equal(lines, tt.wantLines) {
				t.Errorf("lines = %q, want %q", lines, tt.wantLines)
			}
		})
	}
}

// longNote is a note that needs more than the box's line cap at nav width.
const longNote = "Approval to run a python3 script regenerating the golden " +
	"fixtures under testdata/, which rewrites every recorded capture in " +
	"place and cannot be undone without a checkout — recommend declining " +
	"until the working tree is clean and the fixtures are backed up."

func msgBoxModel(t *testing.T, note string) Model {
	t.Helper()
	m := New(&fakeTmux{}, nil, "roost", "cc", "", "claude", nil, nil, 0, ArbiterConfig{})
	m.width, m.height = navWidth, 40
	m.panes = []hub.Pane{{Session: "alpha", ID: "%1",
		Status: hub.StatusNeedsInput, ArbiterNote: note}}
	m.selectedID = "%1"
	return m.syncMsg()
}

func TestMsgBoxWrapsInsteadOfClipping(t *testing.T) {
	m := msgBoxModel(t, longNote)
	box, overflow := m.viewMsgBox(m.width - 4)
	lines := strings.Split(box, "\n")
	if len(lines) != msgBoxLines {
		t.Fatalf("box has %d lines, want %d", len(lines), msgBoxLines)
	}
	if !overflow {
		t.Error("overflow = false, want true for a note past the cap")
	}
	// The clipped footer stopped at "python3 script re"; the box wraps on.
	if !strings.Contains(box, "regenerating") {
		t.Errorf("box = %q, want text past the old truncation point", box)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > m.width-4 {
			t.Errorf("line %q is %d wide, want <= %d", l, w, m.width-4)
		}
	}
}

func TestMsgBoxScrolls(t *testing.T) {
	m := msgBoxModel(t, longNote)
	first, _ := m.viewMsgBox(m.width - 4)

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = mm.(Model)
	if m.msgScroll != 1 {
		t.Fatalf("msgScroll = %d after pgdown, want 1", m.msgScroll)
	}
	if next, _ := m.viewMsgBox(m.width - 4); next == first {
		t.Error("box did not change after pgdown")
	}

	// pgdown stops at the last offset that still fills the box.
	want := m.maxMsgScroll()
	for range 50 {
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		m = mm.(Model)
	}
	if m.msgScroll != want {
		t.Errorf("msgScroll = %d after many pgdowns, want %d", m.msgScroll, want)
	}
	// The bottom of the box is the end of the note — nothing is unreachable.
	if box, _ := m.viewMsgBox(m.width - 4); !strings.Contains(box, "backed up.") {
		t.Errorf("box = %q, want the end of the note at max scroll", box)
	}

	// One pgup from the bottom must move immediately — it would not if
	// pgdown had been allowed to run the offset past the end.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = mm.(Model)
	if m.msgScroll != want-1 {
		t.Errorf("msgScroll = %d after pgup, want %d", m.msgScroll, want-1)
	}
	for range 50 {
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		m = mm.(Model)
	}
	if m.msgScroll != 0 {
		t.Errorf("msgScroll = %d after many pgups, want 0", m.msgScroll)
	}
}

func TestMsgBoxScrollResetsWhenMessageChanges(t *testing.T) {
	m := msgBoxModel(t, longNote)
	m.panes = append(m.panes, hub.Pane{Session: "beta", ID: "%2",
		Status: hub.StatusNeedsInput, ArbiterNote: "a different note"})
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = mm.(Model)
	if m.msgScroll == 0 {
		t.Fatal("msgScroll = 0, want a scrolled box before changing selection")
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mm.(Model)
	if m.msgScroll != 0 {
		t.Errorf("msgScroll = %d after selecting another row, want 0", m.msgScroll)
	}
	if !strings.Contains(m.msg, "a different note") {
		t.Errorf("msg = %q, want the newly selected row's note", m.msg)
	}
}

func TestMsgBoxScrollHintOnlyWhenOverflowing(t *testing.T) {
	m := msgBoxModel(t, longNote)
	if foot := m.viewFooter(); !strings.Contains(foot, "pgup/pgdn scroll") {
		t.Errorf("footer = %q, want the scroll hint while overflowing", foot)
	}
	short := msgBoxModel(t, "suggest 1")
	if foot := short.viewFooter(); strings.Contains(foot, "pgup/pgdn scroll") {
		t.Errorf("footer = %q, want no scroll hint for a note that fits", foot)
	}
}

func TestFooterKeepsHintsDuringError(t *testing.T) {
	m := msgBoxModel(t, "")
	m.actionErr = "kill: can't find pane: alpha"
	m = m.syncMsg()
	foot := m.viewFooter()
	if !strings.Contains(foot, "can't find pane") {
		t.Errorf("footer = %q, want the error", foot)
	}
	if !strings.Contains(foot, "enter focus") {
		t.Errorf("footer = %q, want the hints to survive an error", foot)
	}
}

// A confirm is a question, not a message: it still owns the whole footer.
func TestConfirmReplacesMsgBox(t *testing.T) {
	m := msgBoxModel(t, longNote)
	m.confirmKill = "alpha"
	foot := m.viewFooter()
	if !strings.Contains(foot, "kill alpha?") {
		t.Errorf("footer = %q, want the kill confirm", foot)
	}
	if strings.Contains(foot, "regenerating") {
		t.Errorf("footer = %q, want no message box during a confirm", foot)
	}
}
