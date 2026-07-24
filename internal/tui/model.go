// Package tui is the coop terminal UI: a 1s-polled mirror of the coop
// tmux socket with a live preview pane.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansitrunc "github.com/muesli/reflow/truncate"

	"coop/internal/hub"
)

type tickMsg time.Time

type pollMsg struct {
	panes    []hub.Pane
	hubs     []string // sessions marked @coop — every hub instance's own
	liveGone bool
	title    string // live pane title set during the poll; "" if none
	err      error
}

type focusedMsg struct{ err error }
type sentMsg struct{ err error }
type createdMsg struct{ err error }
type killedMsg struct{ err error }

type livePaneMsg struct {
	id  string
	err error
}

type retargetMsg struct {
	target string
	title  string // title set on the live pane; "" if the set failed
	err    error
}

type resizedMsg struct{ err error }
type borderMsg struct{ err error }

type Model struct {
	tmux       hub.Tmux
	allowed    []string
	hubSession string
	socket     string
	done       *hub.DoneTracker
	notify     *hub.NotifyTracker

	panes      []hub.Pane
	hubNames   []string // hub sessions seen by the last poll (incl. our own)
	selectedID string
	errMsg     string
	actionErr  string

	livePane   string // live preview pane id; "" until adopted/created
	liveTarget string // session the live pane is attached to
	liveTitle  string // last title set on the live pane's border bar
	ensuring   bool   // an ensureLivePane command is in flight
	selfPane   string // the TUI's own pane ($TMUX_PANE); "" disables nav pinning

	confirmKill    string // session armed for killing; "" = disarmed
	confirmQuit    bool   // q pressed once; y confirms quit
	confirmKillAll int    // working-session count at arm time; 0 = disarmed

	focused  bool // nav pane holds terminal focus; drives the frame colour
	showHelp bool // ? pressed; footer shows the full key list

	picking        bool     // repo picker mode
	repos          []string // picker contents, loaded at open
	repoFilter     string   // typed filter; narrows repos by base name
	repoIdx        int      // index into filteredRepos(), not repos
	pendingSession string // session to auto-select when it appears
	focusPending   bool   // focus the preview once it shows the new session
	claudeCmd      string
	loadRepos      func() ([]string, error)

	width, height int
}

func New(tm hub.Tmux, allowed []string, hubSession, socket, selfPane string,
	claudeCmd string, loadRepos func() ([]string, error),
	doneTTL time.Duration) Model {
	return Model{tmux: tm, allowed: allowed, hubSession: hubSession,
		socket: socket, selfPane: selfPane, claudeCmd: claudeCmd,
		loadRepos: loadRepos, done: hub.NewDoneTracker(doneTTL, tm),
		notify:  hub.NewNotifyTracker(tm),
		focused: true, width: 80, height: 24}
}

// LivePane is the live preview pane's id ("" if none) — main kills it on
// exit so the hub session still ends with the TUI.
func (m Model) LivePane() string { return m.livePane }

func (m Model) Init() tea.Cmd { return tea.Batch(m.poll(), tick()) }

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// poll snapshots tmux state off the UI goroutine. It captures the fields
// it needs by value — the Model must not be touched from inside the Cmd.
// It also refreshes the live pane's title bar when status or age drift —
// the one write in an otherwise read-only pass, done here because only
// the poll has fresh statuses. Best-effort: on failure liveTitle stays
// unchanged, so the next tick retries.
func (m Model) poll() tea.Cmd {
	tm, hubSession, live := m.tmux, m.hubSession, m.livePane
	done, notify, liveTarget := m.done, m.notify, m.liveTarget
	selected, lastTitle := m.selectedID, m.liveTitle
	return func() tea.Msg {
		all, err := tm.ListSessions()
		if err != nil {
			return pollMsg{err: err}
		}
		// Hide every hub instance's own session (marked @coop), not
		// just ours — other monitors' hubs are not sessions to manage.
		var panes []hub.Pane
		var hubs []string
		for _, p := range all {
			if !p.Hub {
				panes = append(panes, p)
			} else if !slices.Contains(hubs, p.Session) {
				hubs = append(hubs, p.Session)
			}
		}
		hub.DeriveStatuses(panes, tm.CapturePane)
		for _, p := range notify.Apply(panes) {
			hub.NotifySend(p.Session, cleanTitle(p.Title))
		}
		done.Apply(panes, visitedFunc(tm, hubSession, live, liveTarget), time.Now())
		hub.SortPanes(panes)
		liveGone := live != "" && !paneExists(all, live)
		title := ""
		if live != "" && !liveGone {
			if want := liveTitleFor(panes, pickPane(panes, selected)); want != lastTitle {
				if tm.SetPaneTitle(live, want) == nil {
					title = want
				}
			}
		}
		return pollMsg{panes: panes, hubs: hubs, liveGone: liveGone, title: title}
	}
}

// liveTitleFor is the preview's title bar text for the selected pane.
// The task text (the pane's cleaned title) rides along so what the
// session is doing stays visible while focus is inside the preview.
func liveTitleFor(panes []hub.Pane, selected string) string {
	for _, p := range panes {
		if p.ID == selected {
			s := p.Status.String() + " · " + age(p.Created)
			if task := cleanTitle(p.Title); task != "" {
				s += " · " + task
			}
			return s
		}
	}
	return "no session"
}

// visitedFunc reports whether the user is looking at a session right
// now: either the hub's keyboard focus sits in the live preview pane
// while it shows that session, or a primary client is attached directly
// (the preview's own ignore-size client never counts — selection alone
// must not clear Done). The active-pane lookup is one tmux call, cached
// across a poll; errors read as "not visited" and the TTL still decays.
func visitedFunc(tm hub.Tmux, hubSession, live, liveTarget string) func(string) bool {
	activePane, activeKnown := "", false
	return func(session string) bool {
		if live != "" && liveTarget == session {
			if !activeKnown {
				activePane, _ = tm.ActivePane(hubSession)
				activeKnown = true
			}
			if activePane == live {
				return true
			}
		}
		primary, err := tm.HasPrimaryClient(session)
		return err == nil && primary
	}
}

// paneExists checks the UNFILTERED pane list — the live pane lives in
// the hub session, which the model's list excludes.
func paneExists(panes []hub.Pane, id string) bool {
	for _, p := range panes {
		if p.ID == id {
			return true
		}
	}
	return false
}

// pickPane returns the selected pane id if it still exists, else the top
// pane, else "".
func pickPane(panes []hub.Pane, selected string) string {
	for _, p := range panes {
		if p.ID == selected {
			return selected
		}
	}
	if len(panes) > 0 {
		return panes[0].ID
	}
	return ""
}

// focusCmd moves the client's keyboard focus to the live preview pane —
// the selected session is already attached there, sized to fit.
func (m Model) focusCmd() tea.Cmd {
	tm, pane := m.tmux, m.livePane
	return func() tea.Msg { return focusedMsg{err: tm.SelectPane(pane)} }
}

// ensureLivePane adopts a marked live pane or creates one running the
// placeholder. One command, one message: Update's single-command
// invariant keeps this testable message-by-message.
func (m Model) ensureLivePane() tea.Cmd {
	tm, hubSession := m.tmux, m.hubSession
	return func() tea.Msg {
		id, err := tm.FindMarkedPane(hubSession, hub.LiveMarker)
		if err != nil || id != "" {
			if id != "" && err == nil {
				// Adopted pane may be from a prior run (or creation may
				// have failed to set these before) — re-assert them here.
				err = livePaneOptions(tm, id)
			}
			return livePaneMsg{id: id, err: err}
		}
		if id, err = tm.SplitWindow(hubSession, hub.PlaceholderCmd()); err != nil {
			return livePaneMsg{err: err}
		}
		if err = tm.SetPaneOption(id, hub.LiveMarker, "1"); err != nil {
			return livePaneMsg{err: err}
		}
		return livePaneMsg{id: id, err: livePaneOptions(tm, id)}
	}
}

// livePaneOptions sets the live pane's standing options: respawnability,
// a border bar showing just the title (tmux's default format adds a
// pane index and quotes; the fg pin keeps the text readable when the
// unfocused border dims), and title ownership — the nested attach
// passes the inner app's title escapes through, which would clobber
// the status · age bar.
func livePaneOptions(tm hub.Tmux, id string) error {
	if err := tm.SetPaneOption(id, "remain-on-exit", "on"); err != nil {
		return err
	}
	if err := tm.SetPaneOption(id, "pane-border-format",
		borderFormat(hub.TitleText)); err != nil {
		return err
	}
	return tm.SetPaneOption(id, "allow-set-title", "off")
}

// borderFormat is the live pane's border bar format with the title text
// pinned to the given colour — pinned so the text never inherits the
// dim border style, coloured per focus by setBorderCmd.
func borderFormat(colour string) string {
	return " #[fg=colour" + colour + "]#{pane_title}#[default] "
}

// setBorderCmd drives the preview's focus highlight: the window's
// pane-active-border-style and the bar's title text go amber only while
// the live pane holds focus (the style option is window-scoped, so this
// can't be pinned per-pane). checkActive distinguishes moving into the
// preview from the terminal window losing focus — blur alone doesn't
// say which pane is active.
func (m Model) setBorderCmd(checkActive bool) tea.Cmd {
	tm, hubSession, live := m.tmux, m.hubSession, m.livePane
	return func() tea.Msg {
		colour := hub.BorderDim
		text := hub.TitleText
		if checkActive && live != "" {
			if active, err := tm.ActivePane(hubSession); err == nil && active == live {
				colour, text = hub.FocusAccent, hub.FocusAccent
			}
		}
		if live != "" {
			// Best-effort like the style below — a miss leaves the text
			// colour stale until the next focus flip.
			_ = tm.SetPaneOption(live, "pane-border-format", borderFormat(text))
		}
		return borderMsg{err: tm.SetWindowOption(hubSession,
			"pane-active-border-style", "fg=colour"+colour)}
	}
}

// retarget respawns the live pane onto the current selection — nil if
// there is no live pane yet or it already shows the right session. On
// respawn failure liveTarget stays stale, so the next poll tick retries.
//
// liveTarget lags the actual attach (it only updates once retargetMsg
// comes back), so a poll tick racing an in-flight respawn can dispatch a
// redundant respawn to the same session, and rapid navigation can deliver
// retargetMsg out of order. This is known and accepted: RespawnPane is
// idempotent, and the next poll tick self-heals liveTarget regardless.
func (m Model) retarget() tea.Cmd {
	if m.livePane == "" {
		return nil
	}
	target := ""
	if i := m.indexOf(m.selectedID); i >= 0 {
		target = m.panes[i].Session
	}
	if target == m.liveTarget {
		return nil
	}
	tm, pane, socket := m.tmux, m.livePane, m.socket
	title := liveTitleFor(m.panes, m.selectedID)
	cmd := hub.PlaceholderCmd()
	if target != "" {
		cmd = hub.AttachCmd(socket, target)
	}
	return func() tea.Msg {
		if err := tm.RespawnPane(pane, cmd); err != nil {
			return retargetMsg{err: err}
		}
		// Retitle in the same command so the bar tracks the respawn
		// instead of lagging a poll tick behind it. Best-effort: on
		// failure liveTitle stays stale and the next poll retries.
		msg := retargetMsg{target: target}
		if tm.SetPaneTitle(pane, title) == nil {
			msg.title = title
		}
		return msg
	}
}

// resizeLive sizes the previewed session's window to the live pane, so
// the nested ignore-size client shows it 1:1 after the terminal resizes
// or the preview moves to a session sized for an older window. Sessions
// with a primary client are left alone — that client owns the size.
func (m Model) resizeLive() tea.Cmd {
	if m.livePane == "" || m.liveTarget == "" {
		return nil
	}
	tm, pane, target := m.tmux, m.livePane, m.liveTarget
	return func() tea.Msg {
		primary, err := tm.HasPrimaryClient(target)
		if err != nil || primary {
			return resizedMsg{err: err}
		}
		w, h, err := tm.PaneSize(pane)
		if err != nil {
			return resizedMsg{err: err}
		}
		return resizedMsg{err: tm.ResizeWindow(target, w, h)}
	}
}

// resizeSelf pins the TUI's own pane to navWidth columns once the live
// pane exists, handing the preview every remaining cell — the split's
// 60% default and proportional redistribution on terminal resize both
// leave the nav wider than its fixed-width rows need.
func (m Model) resizeSelf() tea.Cmd {
	if m.selfPane == "" || m.livePane == "" || m.width == navWidth {
		return nil
	}
	tm, pane := m.tmux, m.selfPane
	return func() tea.Msg { return resizedMsg{err: tm.ResizePane(pane, navWidth)} }
}

// nextNeedsInput is the next needs-input pane in display order, scanning
// down from the selection and wrapping. The current row is checked last,
// so repeated presses cycle through every blocked session — answering
// one clears its status, which is all the memory the cycle needs. ""
// means nothing needs input.
func nextNeedsInput(panes []hub.Pane, selectedID string) string {
	start := 0
	for i, p := range panes {
		if p.ID == selectedID {
			start = i + 1
			break
		}
	}
	for i := range panes {
		if p := panes[(start+i)%len(panes)]; p.Status == hub.StatusNeedsInput {
			return p.ID
		}
	}
	return ""
}

func (m Model) indexOf(id string) int {
	for i, p := range m.panes {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Pinning the nav lands as another WindowSizeMsg at navWidth,
		// which falls through to syncing the preview's window size.
		if cmd := m.resizeSelf(); cmd != nil {
			return m, cmd
		}
		return m, m.resizeLive()

	case tickMsg:
		return m, tea.Batch(m.poll(), tick())

	case pollMsg:
		if msg.err != nil {
			m.errMsg = "poll: " + msg.err.Error()
			return m, nil
		}
		m.errMsg = ""
		m.panes = msg.panes
		m.hubNames = msg.hubs
		if msg.title != "" {
			m.liveTitle = msg.title
		}
		m.selectedID = pickPane(m.panes, m.selectedID)
		if m.confirmKill != "" && !m.hasSession(m.confirmKill) {
			m.confirmKill = "" // armed session died on its own
		}
		if m.pendingSession != "" {
			for _, p := range m.panes {
				if p.Session == m.pendingSession {
					m.selectedID = p.ID
					m.pendingSession = ""
					// Focus follows once the retarget chain settles —
					// focusing now would send keys to the old preview.
					m.focusPending = true
					break
				}
			}
		}
		if msg.liveGone {
			m.livePane, m.liveTarget = "", ""
		}
		if m.livePane == "" {
			if m.ensuring {
				return m, nil
			}
			m.ensuring = true
			return m, m.ensureLivePane()
		}
		return m, m.retarget()

	case livePaneMsg:
		m.ensuring = false
		if msg.err != nil {
			m.errMsg = "live pane: " + msg.err.Error()
			return m, nil
		}
		m.livePane, m.liveTarget = msg.id, ""
		// Pin the nav now: the split's WindowSizeMsg raced ahead of this
		// message (livePane was still "" then), so it can't have done it.
		// resizedMsg chains into the retarget.
		if cmd := m.resizeSelf(); cmd != nil {
			return m, cmd
		}
		return m, m.retarget()

	case retargetMsg:
		if msg.err != nil {
			m.errMsg = "live pane: " + msg.err.Error()
			return m, nil
		}
		m.liveTarget = msg.target
		if msg.title != "" {
			m.liveTitle = msg.title
		}
		return m, m.resizeLive()

	case resizedMsg:
		if msg.err != nil {
			m.errMsg = "resize: " + msg.err.Error()
			return m, nil
		}
		// After a nav pin the preview attach is still pending; retarget
		// is a no-op (nil) when the live pane already shows the selection.
		if cmd := m.retarget(); cmd != nil {
			return m, cmd
		}
		// The chain has settled on a just-created session — hand it the
		// keyboard so the user can start typing without pressing enter.
		if m.focusPending && m.livePane != "" {
			m.focusPending = false
			return m, m.focusCmd()
		}
		return m, nil

	case focusedMsg:
		if msg.err != nil {
			m.actionErr = "focus: " + msg.err.Error()
		}
		return m, nil

	case sentMsg:
		if msg.err != nil {
			m.actionErr = msg.err.Error()
		}
		return m, nil

	case createdMsg:
		if msg.err != nil {
			m.actionErr = "create: " + msg.err.Error()
			m.pendingSession = ""
			return m, nil
		}
		// Poll now instead of waiting out the tick — the new session
		// should appear (and take focus) the moment it exists.
		return m, m.poll()

	case killedMsg:
		if msg.err != nil {
			m.actionErr = "kill: " + msg.err.Error()
			return m, nil
		}
		// The rows already left the list on confirm; move the preview
		// off the dead session now rather than on the next poll.
		return m, m.retarget()

	// Focus reporting (tmux focus-events → tea.WithReportFocus): the
	// frame lights up amber exactly while the nav pane holds focus, and
	// the tmux active-border highlight follows focus to the preview.
	// Focus back on the nav means the preview can't be active — dim
	// directly; blur needs the active-pane check.
	case tea.FocusMsg:
		m.focused = true
		return m, m.setBorderCmd(false)

	case tea.BlurMsg:
		m.focused = false
		return m, m.setBorderCmd(true)

	case borderMsg:
		if msg.err != nil {
			m.errMsg = "style: " + msg.err.Error()
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// handleMouse selects the session under a left click. Clicks are inert
// while the picker or a confirm prompt is up — unlike keys they don't
// disarm, so a stray click can't eat a pending confirm.
//
// A click also stands in for the focus flip FocusMsg normally handles:
// tmux's click binding has just made the nav the active pane, but the
// focus-in escape it sends arrives glued to the forwarded click and
// Bubble Tea v1 misparses the pair (unknown CSI, no FocusMsg) — so the
// frame would stay dim and the preview border amber without this.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}
	var borderCmd tea.Cmd
	if !m.focused {
		m.focused = true
		borderCmd = m.setBorderCmd(false)
	}
	if m.picking || m.confirmQuit || m.confirmKillAll > 0 || m.confirmKill != "" {
		return m, borderCmd
	}
	if id := m.paneAt(msg.Y); id != "" {
		m.selectedID = id
		return m, tea.Batch(m.retarget(), borderCmd)
	}
	return m, borderCmd
}

// paneAt maps a terminal row to the pane rendered there, or "" for any
// other row. It mirrors the layout frame() and viewNav() draw: border,
// title line, blank line, then session rows with a header line opening
// each repo group, truncated to leave room for the footer.
func (m Model) paneAt(y int) string {
	if m.height > 0 {
		feet := strings.Split(strings.TrimRight(m.viewFooter(), "\n"), "\n")
		if avail := m.height - 2 - len(feet); y > avail {
			return ""
		}
	}
	row := y - 3 // border + title line + blank line
	repo := ""
	for _, p := range m.panes {
		if r := p.Repo(); r != repo {
			repo = r
			if row == 0 {
				return "" // repo header line
			}
			row--
		}
		if row == 0 {
			return p.ID
		}
		row--
	}
	return ""
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any key means the user is driving the nav again — don't yank the
	// keyboard into the preview under them.
	m.focusPending = false
	if m.picking {
		m.actionErr = ""
		switch msg.String() {
		case "esc":
			// Two-stage: first esc clears the filter, second closes.
			if m.repoFilter != "" {
				m.repoFilter, m.repoIdx = "", 0
			} else {
				m.picking = false
			}
		case "up":
			if m.repoIdx > 0 {
				m.repoIdx--
			}
		case "down":
			if m.repoIdx < len(m.filteredRepos())-1 {
				m.repoIdx++
			}
		case "enter":
			repos := m.filteredRepos()
			if len(repos) == 0 {
				return m, nil
			}
			dir := repos[m.repoIdx]
			m.picking = false
			name := hub.NextSessionName(m.sessionNames(), dir)
			m.pendingSession = name
			return m, m.createCmd(name, dir)
		case "backspace":
			if m.repoFilter != "" {
				r := []rune(m.repoFilter)
				m.repoFilter, m.repoIdx = string(r[:len(r)-1]), 0
			}
		default:
			// Printable keys type into the filter — j/k included, so
			// navigation is arrows-only while the picker is up.
			if msg.Type == tea.KeyRunes && !msg.Alt {
				m.repoFilter += string(msg.Runes)
				m.repoIdx = 0
			}
		}
		return m, nil
	}

	m.actionErr = ""

	// Armed quit confirm: y quits, k kills every session and quits;
	// anything else disarms and is swallowed — q is one slip away from
	// x, so no instant exit. k detours through a second confirm when
	// sessions are still working.
	if m.confirmQuit {
		m.confirmQuit = false
		switch msg.String() {
		case "y":
			return m, tea.Quit
		case "k":
			if n := m.workingSessions(); n > 0 {
				m.confirmKillAll = n
				return m, nil
			}
			return m, m.killAllCmd()
		}
		return m, nil
	}

	// Armed kill-all confirm (sessions were mid-task): y proceeds;
	// anything else disarms and is swallowed.
	if m.confirmKillAll > 0 {
		m.confirmKillAll = 0
		if msg.String() == "y" {
			return m, m.killAllCmd()
		}
		return m, nil
	}

	// Armed kill confirm: y kills; anything else disarms and is
	// swallowed — no surprise navigation while the prompt is up.
	if m.confirmKill != "" {
		name := m.confirmKill
		m.confirmKill = ""
		if msg.String() != "y" {
			return m, nil
		}
		// Drop the session's rows now — waiting a poll tick for the
		// list to reflect the kill reads as "did that work?".
		i := m.indexOf(m.selectedID)
		m.panes = slices.DeleteFunc(m.panes, func(p hub.Pane) bool {
			return p.Session == name
		})
		switch {
		case len(m.panes) == 0:
			m.selectedID = ""
		case i < 0 || i >= len(m.panes):
			m.selectedID = m.panes[len(m.panes)-1].ID
		default:
			m.selectedID = m.panes[i].ID
		}
		return m, m.killCmd(name)
	}

	switch msg.String() {
	case "q":
		m.confirmQuit = true
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if i := m.indexOf(m.selectedID); i > 0 {
			m.selectedID = m.panes[i-1].ID
		}
		return m, m.retarget()
	case "down", "j":
		if i := m.indexOf(m.selectedID); i >= 0 && i < len(m.panes)-1 {
			m.selectedID = m.panes[i+1].ID
		}
		return m, m.retarget()
	case "tab":
		if id := nextNeedsInput(m.panes, m.selectedID); id != "" {
			m.selectedID = id
			return m, m.retarget()
		}
		m.actionErr = "no sessions need input"
	case "enter":
		if m.livePane != "" && m.selectedID != "" {
			return m, m.focusCmd()
		}
	case "n":
		if m.loadRepos == nil {
			m.actionErr = "no repos configured"
			return m, nil
		}
		repos, err := m.loadRepos()
		if err != nil {
			m.actionErr = "config: " + err.Error()
			return m, nil
		}
		if len(repos) == 0 {
			m.actionErr = "config: no repos configured"
			return m, nil
		}
		slices.SortFunc(repos, func(a, b string) int {
			return strings.Compare(filepath.Base(a), filepath.Base(b))
		})
		m.picking = true
		m.repos, m.repoFilter, m.repoIdx = repos, "", 0
	case "x":
		if i := m.indexOf(m.selectedID); i >= 0 {
			m.confirmKill = m.panes[i].Session
		}
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if m.selectedID != "" {
			return m, m.answerCmd(m.selectedID, msg.String())
		}
	case "backspace":
		if m.selectedID != "" {
			return m, m.answerCmd(m.selectedID, "BSpace")
		}
	case "/":
		switch {
		case m.selectedID == "":
			m.actionErr = "no session selected"
		case m.livePane == "":
			m.actionErr = "no live preview"
		default:
			return m, m.slashCmd()
		}
	case "?":
		m.showHelp = !m.showHelp
	}
	return m, nil
}

// answerCmd forwards one bare keypress (a digit or BSpace) to the pane —
// no Enter, since Claude Code dialogs act on the digit itself; outside a
// dialog the digit just lands in the input box, erasable with backspace.
// It re-checks the pane's current command at send time — the poll
// snapshot can be up to 1s stale, and a dead claude leaves a shell that
// must never receive keystrokes.
func (m Model) answerCmd(paneID, key string) tea.Cmd {
	tm, allowed := m.tmux, m.allowed
	return func() tea.Msg {
		cmd, err := tm.PaneCommand(paneID)
		if err != nil {
			return sentMsg{err: err}
		}
		if !slices.Contains(allowed, cmd) {
			return sentMsg{err: fmt.Errorf("refusing to send: pane is running %q (allowed: %s)",
				cmd, strings.Join(allowed, ","))}
		}
		return sentMsg{err: tm.SendKeys(paneID, key)}
	}
}

// slashCmd starts a slash command in the selected session: forward the
// literal "/" (behind the same running-claude gate as answerCmd), then
// move keyboard focus into the live preview so the rest of the command
// is typed there. All-or-nothing — a refused or failed send skips the
// focus, so the keyboard never lands in a preview the "/" didn't reach.
func (m Model) slashCmd() tea.Cmd {
	tm, allowed, paneID, live := m.tmux, m.allowed, m.selectedID, m.livePane
	return func() tea.Msg {
		cmd, err := tm.PaneCommand(paneID)
		if err != nil {
			return sentMsg{err: err}
		}
		if !slices.Contains(allowed, cmd) {
			return sentMsg{err: fmt.Errorf("refusing to send: pane is running %q (allowed: %s)",
				cmd, strings.Join(allowed, ","))}
		}
		if err := tm.SendKeys(paneID, "/"); err != nil {
			return sentMsg{err: err}
		}
		return focusedMsg{err: tm.SelectPane(live)}
	}
}

func (m Model) hasSession(name string) bool {
	for _, p := range m.panes {
		if p.Session == name {
			return true
		}
	}
	return false
}

// killCmd kills the whole tmux session — one pane per session by design,
// and kill-session also covers any stray extra panes.
func (m Model) killCmd(name string) tea.Cmd {
	tm := m.tmux
	return func() tea.Msg { return killedMsg{err: tm.KillSession(name)} }
}

// killAllCmd kills every managed session, then quits. Kills run
// sequentially inside one command so they all land before Bubble Tea
// shuts down; errors are ignored — the TUI is exiting either way.
func (m Model) killAllCmd() tea.Cmd {
	tm := m.tmux
	var names []string
	for _, p := range m.panes {
		if !slices.Contains(names, p.Session) {
			names = append(names, p.Session)
		}
	}
	return func() tea.Msg {
		for _, n := range names {
			_ = tm.KillSession(n)
		}
		return tea.QuitMsg{}
	}
}

// workingSessions counts distinct sessions still mid-task — the ones a
// blanket kill would interrupt.
func (m Model) workingSessions() int {
	var names []string
	for _, p := range m.panes {
		if p.Status == hub.StatusWorking && !slices.Contains(names, p.Session) {
			names = append(names, p.Session)
		}
	}
	return len(names)
}

// sessionNames is every session the model can see plus every hub
// instance's own (ours as a floor — the first poll may not have run)
// and any just-created session the poll hasn't shown yet — the
// collision set for naming a new session.
func (m Model) sessionNames() []string {
	names := append([]string{m.hubSession}, m.hubNames...)
	if m.pendingSession != "" {
		names = append(names, m.pendingSession)
	}
	for _, p := range m.panes {
		names = append(names, p.Session)
	}
	return names
}

// createCmd sizes the new session to the live preview pane — a detached
// session has no client to size it and would default to 80x24, showing
// tiny in the preview. Best-effort: 0,0 falls back to tmux's default.
func (m Model) createCmd(name, dir string) tea.Cmd {
	tm, cmd, live := m.tmux, m.claudeCmd, m.livePane
	return func() tea.Msg {
		w, h := 0, 0
		if live != "" {
			w, h, _ = tm.PaneSize(live)
		}
		return createdMsg{err: tm.NewSession(name, dir, cmd, w, h)}
	}
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	cursorStyle = lipgloss.NewStyle().Bold(true)
	needsStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	doneStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	workStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	idleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	footStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	frameFocus = lipgloss.NewStyle().Foreground(lipgloss.Color(hub.FocusAccent))
	frameBlur  = lipgloss.NewStyle().Foreground(lipgloss.Color(hub.BorderDim))
)

// statusStyle is the row's status colour — glyph and title both render
// in it, so the colour alone says what the session is doing.
func statusStyle(s hub.Status) lipgloss.Style {
	switch s {
	case hub.StatusNeedsInput:
		return needsStyle
	case hub.StatusDone:
		return doneStyle
	case hub.StatusWorking:
		return workStyle
	default:
		return idleStyle
	}
}

// statusGlyph is the row's one-cell status marker: ● for the active
// statuses, ○ for idle. statusStyle's colour carries the distinction.
func statusGlyph(s hub.Status) string {
	switch s {
	case hub.StatusNeedsInput, hub.StatusDone, hub.StatusWorking:
		return "●"
	default:
		return "○"
	}
}

// titleWidth is the row's title column, in display cells.
const titleWidth = 32

// navWidth is the nav pane's pinned width: cursor (2) + glyph and its
// gap (2) + title column (titleWidth+1) + age ("12h34m"), plus 4 cells
// for the frame's borders and padding — the longest row fits exactly.
const navWidth = 47

// truncate caps s at n runes, ending in … when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// pad right-pads s to n display columns. fmt's %-*s counts bytes, which
// misaligns styled or non-ASCII text; lipgloss.Width sees through both.
func pad(s string, n int) string {
	if d := n - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// tildify collapses the home-directory prefix to ~ so repo paths fit
// the nav's width.
func tildify(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, home); ok && (rest == "" || rest[0] == '/') {
		return "~" + rest
	}
	return p
}

// cleanTitle strips the title's leading status glyph (✳/✻ idle marks,
// braille spinner frame, 🔔) and padding — the status column already
// carries that signal.
func cleanTitle(title string) string {
	for title != "" {
		r, size := utf8.DecodeRuneInString(title)
		if r != '✳' && r != '✻' && r != '🔔' &&
			!(r >= 0x2800 && r <= 0x28FF) && !unicode.IsSpace(r) {
			break
		}
		title = title[size:]
	}
	return title
}

func age(created time.Time) string {
	if created.IsZero() {
		return "-"
	}
	d := time.Since(created).Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// frame boxes body above foot in a rounded border filling w×h cells,
// title embedded in the top edge and foot pinned to the bottom one.
// Amber when focused, dim otherwise — the same palette tmux paints the
// preview's border with, so exactly one panel is lit at a time. Body
// rows that don't fit above the footer are dropped.
func frame(title, body, foot string, focused bool, w, h int) string {
	st := frameBlur
	if focused {
		st = frameFocus
	}
	if w < 8 {
		w = 8
	}
	if h < 4 {
		h = 4
	}
	inner := w - 4 // "│ " left, " │" right
	top := ansitrunc.String("─ "+title+" ", uint(w-2))
	top += strings.Repeat("─", w-2-lipgloss.Width(top))
	feet := strings.Split(strings.TrimRight(foot, "\n"), "\n")
	if len(feet) > h-2 {
		feet = feet[:h-2]
	}
	rows := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if avail := h - 2 - len(feet); len(rows) > avail {
		rows = rows[:avail]
	} else {
		for len(rows) < avail {
			rows = append(rows, "")
		}
	}
	var b strings.Builder
	b.WriteString(st.Render("╭"+top+"╮") + "\n")
	side := st.Render("│")
	for _, r := range append(rows, feet...) {
		b.WriteString(side + " " + pad(ansitrunc.String(r, uint(inner)), inner) + " " + side + "\n")
	}
	b.WriteString(st.Render("╰" + strings.Repeat("─", w-2) + "╯"))
	return b.String()
}

func (m Model) View() string {
	if m.picking {
		esc := "esc cancel"
		if m.repoFilter != "" {
			esc = "esc clear"
		}
		return frame("new session", m.viewPicker(),
			flowHints([]string{"type to filter", "↑/↓ select", "enter create", esc},
				m.width-4),
			m.focused, m.width, m.height)
	}
	return frame("sessions", m.viewNav(), m.viewFooter(),
		m.focused, m.width, m.height)
}

func (m Model) viewNav() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("coop") + "  " +
		footStyle.Render(fmt.Sprintf("%d sessions", len(m.panes))) + "\n\n")

	if len(m.panes) == 0 {
		b.WriteString("no sessions — start one by using the n key\n")
	}
	repo := ""
	for _, p := range m.panes {
		if r := p.Repo(); r != repo {
			repo = r
			b.WriteString(titleStyle.Render(repo) + "\n")
		}
		st := statusStyle(p.Status)
		line := pad(st.Render(statusGlyph(p.Status)+" "+
			truncate(cleanTitle(p.Title), titleWidth)), titleWidth+3) +
			age(p.Created)
		if p.ID == m.selectedID {
			b.WriteString(cursorStyle.Render("▸ "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

// filteredRepos narrows the picker list to repos whose base name
// contains the typed filter, case-insensitively. Order is preserved.
func (m Model) filteredRepos() []string {
	if m.repoFilter == "" {
		return m.repos
	}
	f := strings.ToLower(m.repoFilter)
	var out []string
	for _, r := range m.repos {
		if strings.Contains(strings.ToLower(filepath.Base(r)), f) {
			out = append(out, r)
		}
	}
	return out
}

func (m Model) viewPicker() string {
	var b strings.Builder
	if m.repoFilter == "" {
		b.WriteString(footStyle.Render("pick a repo — type to filter") + "\n\n")
	} else {
		b.WriteString(footStyle.Render("filter: ") + m.repoFilter + "▌\n\n")
	}
	repos := m.filteredRepos()
	if len(repos) == 0 {
		b.WriteString(footStyle.Render("no matches") + "\n")
	}
	for i, r := range repos {
		name := titleStyle.Render(filepath.Base(r))
		if i == m.repoIdx {
			b.WriteString(cursorStyle.Render("▸ ") + name + "\n")
		} else {
			b.WriteString("  " + name + "\n")
		}
		b.WriteString("  " + footStyle.Render(tildify(r)) + "\n")
	}
	return b.String()
}

func (m Model) viewFooter() string {
	if m.confirmQuit {
		return errStyle.Render("quit coop? · y quit · k kill all & quit · esc cancel")
	}
	if m.confirmKillAll > 0 {
		word := "sessions"
		if m.confirmKillAll == 1 {
			word = "session"
		}
		return errStyle.Render(fmt.Sprintf("%d %s still working — kill all & quit? · y confirm · esc cancel",
			m.confirmKillAll, word))
	}
	if m.confirmKill != "" {
		return errStyle.Render("kill " + m.confirmKill + "? · y confirm · esc cancel")
	}
	if m.actionErr != "" {
		return errStyle.Render(m.actionErr)
	}
	if m.errMsg != "" {
		return errStyle.Render(m.errMsg)
	}
	hints := []string{"↑/↓ select", "enter focus", "n new", "? help"}
	if m.showHelp {
		hints = []string{"↑/↓ select", "enter focus", "tab next input",
			"shift+←/→ switch pane", "0-9 answer", "bksp erase",
			"/ command", "n new", "x kill", "q quit", "? close"}
	}
	return flowHints(hints, m.width-4) // frame() draws "│ " and " │"
}

// flowHints packs hint items into "·"-separated lines that fit width, so
// the frame never clips a hint mid-word on narrow panes.
func flowHints(items []string, width int) string {
	var lines []string
	line := ""
	for _, it := range items {
		switch {
		case line == "":
			line = it
		case lipgloss.Width(line)+3+lipgloss.Width(it) <= width:
			line += " · " + it
		default:
			lines = append(lines, footStyle.Render(line))
			line = it
		}
	}
	lines = append(lines, footStyle.Render(line))
	return strings.Join(lines, "\n")
}
