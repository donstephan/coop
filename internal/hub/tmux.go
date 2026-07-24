// Package hub mirrors a tmux socket's panes and drives them: listing
// sessions, deriving Claude Code status, capturing screens, and sending
// keys.
package hub

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tmux is the subset of the tmux CLI the hub needs. Faked in unit tests;
// ExecTmux is the real thing.
type Tmux interface {
	CapturePane(pane string) (string, error)
	SendKeys(pane string, keys ...string) error
	PaneCommand(pane string) (string, error)
	ListSessions() ([]Pane, error)
	SelectPane(pane string) error
	NewSession(name, dir, cmd string, width, height int) error
	PaneSize(pane string) (int, int, error)
	SplitWindow(target, cmd string) (string, error)
	RespawnPane(pane, cmd string) error
	ResizePane(pane string, width int) error
	ResizeWindow(session string, width, height int) error
	HasPrimaryClient(session string) (bool, error)
	ActivePane(session string) (string, error)
	KillPane(pane string) error
	KillSession(name string) error
	SetPaneOption(pane, name, value string) error
	UnsetPaneOption(pane, name string) error
	SetWindowOption(session, name, value string) error
	SetServerOption(name, value string) error
	SetPaneTitle(pane, title string) error
	FindMarkedPane(session, option string) (string, error)
}

// Pane is one tmux pane as reported by a single list-panes poll.
// The TUI's model is a slice of these — nothing else is stored.
type Pane struct {
	Session string
	ID      string // tmux pane id, e.g. "%0"
	PID     int    // pane_pid — keys Claude Code's session state file
	Title   string // pane_title — Claude Code encodes its state here
	Bell    bool   // window_bell_flag — set when the pane rang the bell
	Cmd     string // pane_current_command
	Path    string // session_path — the session's start directory
	Created time.Time
	Status  Status // filled by DeriveStatuses, not by parsing

	// Claude Code's own published state for this pane's process, when it
	// publishes one (see ClaudeSessions). Nil for everything else.
	Claude *ClaudeState

	// Stats from the session's transcript, filled only when the TUI is
	// showing a column that needs them (see Transcripts). Nil otherwise.
	Stats *TranscriptStats

	// Shared cross-hub state, read from user options each poll so every
	// hub instance on the socket sees the same thing.
	Hub          bool      // @coop on the session — a hub's own session
	WorkingMark  bool      // @coop_working — pane was working last poll
	DoneSince    time.Time // @coop_done_since — when it finished (zero if unset)
	NotifiedMark bool      // @coop_notified — this needs-input episode was announced
}

// Repo is the pane's repo group: the session start directory's basename.
// Grouping on session_path rather than the session name survives the
// name-collision suffixes NextSessionName adds ("coop-2").
func (p Pane) Repo() string {
	if p.Path == "" {
		return "?"
	}
	return filepath.Base(p.Path)
}

// ExecTmux shells out to the tmux client on a dedicated socket (-L).
// The tmux client and server versions must match (protocol coupling).
type ExecTmux struct {
	Socket string
}

func (t *ExecTmux) run(args ...string) (string, error) {
	full := append([]string{"-L", t.Socket}, args...)
	out, err := exec.Command("tmux", full...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (t *ExecTmux) CapturePane(pane string) (string, error) {
	// -p print, -e keep ANSI escapes, -J join wrapped lines
	return t.run("capture-pane", "-p", "-e", "-J", "-t", pane)
}

func (t *ExecTmux) SendKeys(pane string, keys ...string) error {
	_, err := t.run(append([]string{"send-keys", "-t", pane}, keys...)...)
	return err
}

func (t *ExecTmux) PaneCommand(pane string) (string, error) {
	out, err := t.run("display-message", "-p", "-t", pane, "#{pane_current_command}")
	return strings.TrimSpace(out), err
}

// User options carrying cross-hub shared state. HubMarker is set on a
// hub instance's own session; the others live on tracked panes and back
// the done-badge (see DoneTracker) and needs-input notification dedupe
// (see NotifyTracker).
const (
	HubMarker       = "@coop"
	WorkingMarker   = "@coop_working"
	DoneSinceMarker = "@coop_done_since"
	NotifiedMarker  = "@coop_notified"
)

// \x1f (unit separator) can't appear in titles or session names; \t can.
// The trailing user options render as "" when unset.
const paneFormat = "#{session_name}\x1f#{pane_id}\x1f#{pane_pid}\x1f#{pane_title}\x1f#{window_bell_flag}\x1f#{pane_current_command}\x1f#{session_created}\x1f#{session_path}\x1f#{" + HubMarker + "}\x1f#{" + WorkingMarker + "}\x1f#{" + DoneSinceMarker + "}\x1f#{" + NotifiedMarker + "}"

// unixTime parses a "#{...}" unix-seconds field; anything unparseable
// (including unset = "") reads as the zero time.
func unixTime(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

func parsePanes(out string) []Pane {
	var panes []Pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 12 {
			continue
		}
		pid, _ := strconv.Atoi(f[2]) // unreadable pid = 0 = no state lookup
		panes = append(panes, Pane{
			Session: f[0], ID: f[1], PID: pid, Title: f[3],
			Bell: f[4] == "1", Cmd: f[5], Created: unixTime(f[6]), Path: f[7],
			Hub: f[8] == "1", WorkingMark: f[9] == "1", DoneSince: unixTime(f[10]),
			NotifiedMark: f[11] == "1",
		})
	}
	return panes
}

// SessionInfo is one tmux session as reported by Sessions — what the
// launcher needs to reuse a detached hub or pick a fresh name.
type SessionInfo struct {
	Name     string
	Attached bool // a client is attached right now
	Hub      bool // marked @coop — a hub instance's own session
}

const sessionFormat = "#{session_name}\x1f#{session_attached}\x1f#{" + HubMarker + "}"

func parseSessions(out string) []SessionInfo {
	var sessions []SessionInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 3 {
			continue
		}
		sessions = append(sessions, SessionInfo{
			Name: f[0], Attached: f[1] != "0" && f[1] != "", Hub: f[2] == "1",
		})
	}
	return sessions
}

// Sessions lists every session on the socket. No server is a normal
// state and reads as no sessions.
func (t *ExecTmux) Sessions() ([]SessionInfo, error) {
	out, err := t.run("list-sessions", "-F", sessionFormat)
	if err != nil {
		if isNoServer(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseSessions(out), nil
}

func (t *ExecTmux) ListSessions() ([]Pane, error) {
	out, err := t.run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		if isNoServer(err) {
			return nil, nil // no server = no sessions; a normal state
		}
		return nil, err
	}
	return parsePanes(out), nil
}

// SelectPane makes the pane the active one in its window, moving the
// attached client's keyboard focus to it.
func (t *ExecTmux) SelectPane(pane string) error {
	_, err := t.run("select-pane", "-t", pane)
	return err
}

// NewSession creates a detached session named name, cwd dir, running
// cmd. No -f: server config only matters at first server start, and
// the hub's server is already running. A detached session has no client
// to size it (and the live pane's nested client uses ignore-size), so
// width/height set its size explicitly; 0,0 keeps tmux's default.
func (t *ExecTmux) NewSession(name, dir, cmd string, width, height int) error {
	args := []string{"new-session", "-d", "-s", name, "-c", dir}
	if width > 0 && height > 0 {
		args = append(args, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
	}
	_, err := t.run(append(args, cmd)...)
	return err
}

// PaneSize returns a pane's width and height in cells.
func (t *ExecTmux) PaneSize(pane string) (int, int, error) {
	out, err := t.run("display-message", "-p", "-t", pane,
		"#{pane_width} #{pane_height}")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("pane size: unexpected output %q", strings.TrimSpace(out))
	}
	w, werr := strconv.Atoi(f[0])
	h, herr := strconv.Atoi(f[1])
	if werr != nil || herr != nil {
		return 0, 0, fmt.Errorf("pane size: unexpected output %q", strings.TrimSpace(out))
	}
	return w, h, nil
}

// SplitWindow splits to the right of the target window, running cmd,
// without stealing focus; returns the new pane's id.
func (t *ExecTmux) SplitWindow(target, cmd string) (string, error) {
	out, err := t.run("split-window", "-h", "-d", "-l", "60%",
		"-P", "-F", "#{pane_id}", "-t", target, cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (t *ExecTmux) RespawnPane(pane, cmd string) error {
	_, err := t.run("respawn-pane", "-k", "-t", pane, cmd)
	return err
}

// ResizePane sets a pane's width in cells; its sibling absorbs the rest.
func (t *ExecTmux) ResizePane(pane string, width int) error {
	_, err := t.run("resize-pane", "-t", pane, "-x", strconv.Itoa(width))
	return err
}

// ResizeWindow forces the session's current window to width x height —
// for preview-only sessions, whose sole client ignores size and so never
// sizes the window itself. resize-window flips window-size to manual, so
// restore latest right after: the forced size sticks while only
// ignore-size clients are attached, and a primary client attaching later
// resizes the window as usual.
func (t *ExecTmux) ResizeWindow(session string, width, height int) error {
	// "=session:" — exact session match, current window. The trailing
	// colon matters: these take a target-window, where a bare "=name" is
	// an exact WINDOW-name match and set-option fails with
	// "no such window".
	target := "=" + session + ":"
	if _, err := t.run("resize-window", "-t", target,
		"-x", strconv.Itoa(width), "-y", strconv.Itoa(height)); err != nil {
		return err
	}
	_, err := t.run("set-option", "-w", "-t", target, "window-size", "latest")
	return err
}

// HasPrimaryClient reports whether any client besides the hub's
// ignore-size preview client is attached to the session.
func (t *ExecTmux) HasPrimaryClient(session string) (bool, error) {
	out, err := t.run("list-clients", "-t", "="+session, "-F", "#{client_flags}")
	if err != nil {
		return false, err
	}
	return parseClientFlags(out), nil
}

// SessionForPane returns the name of the session holding the pane —
// how a hub instance learns its own session name at startup.
func (t *ExecTmux) SessionForPane(pane string) (string, error) {
	out, err := t.run("display-message", "-p", "-t", pane, "#{session_name}")
	return strings.TrimSpace(out), err
}

// ActivePane returns the id of the session's current window's active
// pane — which pane in that session holds the keyboard focus.
func (t *ExecTmux) ActivePane(session string) (string, error) {
	out, err := t.run("display-message", "-p", "-t", "="+session+":", "#{pane_id}")
	return strings.TrimSpace(out), err
}

func (t *ExecTmux) KillPane(pane string) error {
	_, err := t.run("kill-pane", "-t", pane)
	return err
}

// KillSession kills the whole session by exact name ("=" prefix — a bare
// name is a prefix match and could kill a sibling like "coop-2").
func (t *ExecTmux) KillSession(name string) error {
	_, err := t.run("kill-session", "-t", "="+name)
	return err
}

func (t *ExecTmux) SetPaneOption(pane, name, value string) error {
	_, err := t.run("set-option", "-p", "-t", pane, name, value)
	return err
}

func (t *ExecTmux) UnsetPaneOption(pane, name string) error {
	_, err := t.run("set-option", "-pu", "-t", pane, name)
	return err
}

// SetSessionOption sets a session option — used to mark a hub's own
// session with HubMarker. No "=" here: set-option's session target
// rejects the exact-match prefix (tmux 3.6), so this relies on tmux
// preferring an exact name match ("roost" never resolves to "roost-2").
func (t *ExecTmux) SetSessionOption(session, name, value string) error {
	_, err := t.run("set-option", "-t", session+":", name, value)
	return err
}

// SetWindowOption sets a window option on the session's current window
// (same "=session:" targeting as ResizeWindow).
func (t *ExecTmux) SetWindowOption(session, name, value string) error {
	_, err := t.run("set-option", "-w", "-t", "="+session+":", name, value)
	return err
}

func (t *ExecTmux) SetServerOption(name, value string) error {
	_, err := t.run("set-option", "-s", name, value)
	return err
}

// SetPaneTitle sets the pane's title (what #{pane_title} shows in its
// border bar) without moving focus — select-pane -T only retitles.
func (t *ExecTmux) SetPaneTitle(pane, title string) error {
	_, err := t.run("select-pane", "-t", pane, "-T", title)
	return err
}

// FindMarkedPane returns the first pane in the session whose user option
// (e.g. @coop_live) is "1", or "" if none.
func (t *ExecTmux) FindMarkedPane(session, option string) (string, error) {
	out, err := t.run("list-panes", "-s", "-t", session,
		"-F", "#{pane_id}\x1f#{"+option+"}")
	if err != nil {
		return "", err
	}
	return parseMarkedPane(out), nil
}

// isNoServer reports whether a tmux client error just means the server
// (and therefore every pane) is gone — a normal state, not a failure.
func isNoServer(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting to")
}
