// Package tui is the coop terminal UI: a 1s-polled mirror of the coop
// tmux socket with a live preview pane.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansitrunc "github.com/muesli/reflow/truncate"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"

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

// repoAddedMsg is the picker's add row landing in the config: dir is the
// directory the typed path resolved to, ready to start a session in.
type repoAddedMsg struct {
	dir string
	err error
}

type Model struct {
	tmux       hub.Tmux
	allowed    []string
	hubSession string
	socket     string
	done       *hub.DoneTracker
	notify     *hub.NotifyTracker
	nudge      *hub.ArbiterNudger
	claude     *hub.ClaudeSessions // Claude Code's own per-process state
	// transcripts reads session transcripts for the stat column; a field
	// rather than a concrete type so tests can substitute one.
	transcripts func(sessionID, cwd string) (hub.TranscriptStats, bool)
	statCol     statColumn

	panes      []hub.Pane
	hubNames   []string // hub sessions seen by the last poll (incl. our own)
	selectedID string
	errMsg     string
	actionErr  string

	msg       string // text currently in the footer message box
	msgScroll int    // message box scroll offset, in wrapped lines

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

	picking    bool     // repo picker mode
	repos      []string // picker contents, loaded at open
	repoFilter string   // typed filter; narrows repos by base name
	// repoIdx indexes filteredRepos(), not repos — except for one past
	// its end, which is the "+ add new repo" row pinned below the list.
	repoIdx        int
	adding         bool   // add-repo prompt is up, over the picker
	repoPath       string // path typed into that prompt
	pendingSession string // session to auto-select when it appears
	focusPending   bool   // focus the preview once it shows the new session
	claudeCmd      string
	loadRepos      func() ([]string, error)
	// addRepo writes a repo into the config and returns the directory it
	// resolved to; nil when there is no config to write.
	addRepo func(repo string) (string, error)
	arb     ArbiterConfig

	width, height int
}

// ArbiterConfig is what the a key needs to launch the arbiter session.
// The zero value disables launching (a reports the missing config).
type ArbiterConfig struct {
	Model     string // claude model id/alias; "" means sonnet
	ConfigDir string // dir holding arbiter.md and the arbiter/ workdir
}

func New(tm hub.Tmux, allowed []string, hubSession, socket, selfPane string,
	claudeCmd string, loadRepos func() ([]string, error),
	addRepo func(string) (string, error),
	doneTTL time.Duration, arb ArbiterConfig) Model {
	return Model{tmux: tm, allowed: allowed, hubSession: hubSession,
		socket: socket, selfPane: selfPane, claudeCmd: claudeCmd,
		loadRepos: loadRepos, addRepo: addRepo, done: hub.NewDoneTracker(doneTTL, tm),
		notify: hub.NewNotifyTracker(tm), nudge: hub.NewArbiterNudger(tm, hubSession),
		claude:      hub.DefaultClaudeSessions(),
		transcripts: hub.DefaultTranscripts().Stats,
		arb:         arb,
		focused:     true, width: 80, height: 24}
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
	nudge := m.nudge
	claude, transcripts := m.claude, m.transcripts
	if m.statCol == statColOff {
		transcripts = nil // nobody is looking; don't touch the filesystem
	}
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
		hub.AttachClaudeState(panes, claude.Lookup)
		hub.AttachTranscriptStats(panes, transcripts)
		hub.DeriveStatuses(panes, tm.CapturePane)
		for _, p := range notify.Apply(panes) {
			hub.NotifySend(p.Session, cleanTitle(p.Title))
		}
		nudge.Apply(panes, hubs)
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
			s := p.Status.String() + " · " + age(p.Since())
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
			// Report the id anyway: an unmarked pane the model has
			// forgotten is one FindMarkedPane can never adopt, so the
			// next tick would split another.
			return livePaneMsg{id: id, err: err}
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
	// allow-set-title arrived in tmux 3.5; an older server rejects it as
	// an invalid option. Best-effort, since the fallback is only that the
	// inner app can overwrite the border bar until the next poll retitles
	// it — not worth refusing to preview at all (Ubuntu 24.04 ships 3.4).
	_ = tm.SetPaneOption(id, "allow-set-title", "off")
	return nil
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
	if m.selfPane == "" || m.livePane == "" || m.width == m.navCols() {
		return nil
	}
	tm, pane, cols := m.tmux, m.selfPane, m.navCols()
	return func() tea.Msg { return resizedMsg{err: tm.ResizePane(pane, cols)} }
}

// nextNeedsInput is the next needs-input pane in display order, scanning
// down from the selection and wrapping. The current row is checked last,
// so repeated presses cycle through every blocked session — answering
// one clears its status, which is all the memory the cycle needs. ""
// means nothing needs input.
//
// The arbiter is never a target: tab is for the work that is blocked on
// you, and an arbiter at its own permission prompt is coop's problem,
// not a session's. Its pinned row shows the status; arrow to it.
func nextNeedsInput(panes []hub.Pane, selectedID string) string {
	start := 0
	for i, p := range panes {
		if p.ID == selectedID {
			start = i + 1
			break
		}
	}
	for i := range panes {
		if p := panes[(start+i)%len(panes)]; p.Status == hub.StatusNeedsInput && !p.Arbiter {
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

// Update wraps update so the message box stays in sync from one place
// rather than from every one of update's return sites. It adds no command
// of its own, so the single-command invariant holds.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next.(Model).syncMsg(), cmd
}

// syncMsg recomputes the footer message and rewinds the box whenever the
// text changes — a new selection, a new error or a cleared note should
// always start at the top.
func (m Model) syncMsg() Model {
	text, _ := m.message()
	if text != m.msg {
		m.msg, m.msgScroll = text, 0
	}
	return m
}

// message is the footer message box's contents: the most urgent of the
// action error, the poll error and the selected row's arbiter line, with
// the style it renders in.
func (m Model) message() (string, lipgloss.Style) {
	switch {
	case m.actionErr != "":
		return m.actionErr, errStyle
	case m.errMsg != "":
		return m.errMsg, errStyle
	}
	return m.arbiterDetail()
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	case repoAddedMsg:
		if msg.err != nil {
			// Prompt stays up, text intact — this is usually a typo.
			m.actionErr = "add repo: " + msg.err.Error()
			return m, nil
		}
		m.picking, m.adding, m.repoPath, m.repoFilter = false, false, "", ""
		name := hub.NextSessionName(m.sessionNames(), msg.dir)
		m.pendingSession = name
		return m, m.createCmd(name, msg.dir)

	case livePaneMsg:
		m.ensuring = false
		if msg.err != nil {
			m.errMsg = "live pane: " + msg.err.Error()
		}
		// A pane that exists is adopted even when something else in the
		// same command failed: forgetting it here means the next poll
		// finds no live pane and splits another, once per tick.
		if msg.id == "" {
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

// handleMouse selects the row under a left click — a session in the nav,
// a repo or the add row in the picker. A click only ever moves the
// cursor; enter is what acts, so a stray click can't start or kill
// anything. Clicks are inert while a confirm prompt is up, since unlike
// keys they don't disarm and would otherwise eat the pending confirm.
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
	if m.confirmQuit || m.confirmKillAll > 0 || m.confirmKill != "" {
		return m, borderCmd
	}
	if m.picking {
		// The add prompt has no rows to click, only a text field.
		if !m.adding {
			if i := m.pickerRowAt(msg.Y); i >= 0 {
				m.repoIdx = i
			}
		}
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
// each repo group and a blank line plus divider opening the pinned
// arbiter section, truncated to leave room for the footer.
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
		if p.Arbiter {
			if row <= 1 {
				return "" // blank line, then the arbiter divider
			}
			row -= 2
		} else if r := p.Repo(); r != repo {
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

// handleAddKey drives the add-repo prompt reached from the picker's last
// row. The path is typed blind — there is no completion — so backspace
// and esc are the whole edit vocabulary. A rejected path (see AddRepo)
// leaves the prompt up with the text intact so it can be corrected.
func (m Model) handleAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.adding, m.repoPath = false, ""
	case "enter":
		if strings.TrimSpace(m.repoPath) == "" {
			return m, nil
		}
		if m.addRepo == nil {
			m.actionErr = "no config file to add to"
			return m, nil
		}
		return m, m.addRepoCmd(m.repoPath)
	case "backspace":
		if m.repoPath != "" {
			r := []rune(m.repoPath)
			m.repoPath = string(r[:len(r)-1])
		}
	case " ":
		m.repoPath += " "
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.repoPath += string(msg.Runes)
		}
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any key means the user is driving the nav again — don't yank the
	// keyboard into the preview under them.
	m.focusPending = false
	if m.picking {
		m.actionErr = ""
		if m.adding {
			return m.handleAddKey(msg)
		}
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
			// One past the last repo is the add row, so it is reachable
			// even when the filter matches nothing.
			if m.repoIdx < len(m.filteredRepos()) {
				m.repoIdx++
			}
		case "enter":
			repos := m.filteredRepos()
			if m.repoIdx >= len(repos) {
				m.adding, m.repoPath = true, ""
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
	case "pgup":
		m.msgScroll = max(0, m.msgScroll-1)
	case "pgdown":
		// Clamp here as well as in wrapMsg: letting the offset run past the
		// end would leave pgup pressed silently that many times over.
		m.msgScroll = min(m.msgScroll+1, m.maxMsgScroll())
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
		// An empty list still opens the picker: its add row is the only
		// way to configure the first repo.
		slices.SortFunc(repos, func(a, b string) int {
			return strings.Compare(filepath.Base(a), filepath.Base(b))
		})
		m.picking = true
		m.adding, m.repoPath = false, ""
		m.repos, m.repoFilter, m.repoIdx = repos, "", 0
	case "x":
		if i := m.indexOf(m.selectedID); i >= 0 {
			m.confirmKill = m.panes[i].Session
		}
	case "s":
		// Widening the nav is the only tmux work; the stats themselves
		// land on the next poll tick.
		m.statCol = m.statCol.next()
		return m, m.resizeSelf()
	case "a":
		// Cycle off → recommend → full → off, the s-key precedent. The
		// session's existence IS the enabled state; mode lives on it.
		if arb, ok := hub.FindArbiter(m.panes); ok {
			if hub.ArbiterModeOf(arb) == hub.ArbiterModeRecommend {
				return m, m.arbiterModeCmd(arb.Session, hub.ArbiterModeFull)
			}
			m.confirmKill = arb.Session // full → off: same y/esc confirm as x
			return m, nil
		}
		if m.arb.ConfigDir == "" {
			m.actionErr = "arbiter: no config dir"
			return m, nil
		}
		return m, m.arbiterCreateCmd()
	case " ":
		// Apply the arbiter's suggestion: the same send the digit key
		// makes, minus reading the number out of the note. Not gated on
		// mode — a full-mode arbiter that escalated instead of answering
		// still leaves a digit worth one key.
		if s := m.selectedSuggest(); s != "" {
			return m, m.answerCmd(m.selectedID, s)
		}
		m.actionErr = "no arbiter suggestion for this session"
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

// addRepoCmd writes the typed path into the config off the UI goroutine,
// like every other bit of I/O here — the Model does not touch the disk.
func (m Model) addRepoCmd(repo string) tea.Cmd {
	add := m.addRepo
	return func() tea.Msg {
		dir, err := add(repo)
		return repoAddedMsg{dir: dir, err: err}
	}
}

// arbiterModeCmd flips the arbiter's mode option. Best-effort: the
// footer shows the mode from poll data, so a miss self-heals next tick.
func (m Model) arbiterModeCmd(session, mode string) tea.Cmd {
	tm := m.tmux
	return func() tea.Msg {
		return sentMsg{err: tm.SetSessionOption(session, hub.ArbiterModeMarker, mode)}
	}
}

// arbiterCreateCmd launches the arbiter session, sized to the live
// preview like createCmd — same createdMsg path, so a failure lands in
// actionErr and success triggers an immediate poll.
func (m Model) arbiterCreateCmd() tea.Cmd {
	tm, arb, claudeCmd, live := m.tmux, m.arb, m.claudeCmd, m.livePane
	allowed := strings.Join(m.allowed, ",")
	return func() tea.Msg {
		w, h := 0, 0
		if live != "" {
			w, h, _ = tm.PaneSize(live)
		}
		model := arb.Model
		if model == "" {
			model = "sonnet"
		}
		return createdMsg{err: hub.LaunchArbiter(tm, arb.ConfigDir, allowed, claudeCmd, model, w, h)}
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

// statusGlyph is the row's one-cell status marker, a distinct shape per
// status: ◆ needs input, ● working, ✓ done, ○ idle. statusStyle's colour
// reinforces the shape rather than carrying the distinction alone — a
// scan shouldn't depend on telling two hues apart. All four must stay
// one cell wide or the columns to their right shear (see TestStatusGlyph).
func statusGlyph(s hub.Status) string {
	switch s {
	case hub.StatusNeedsInput:
		return "◆"
	case hub.StatusDone:
		return "✓"
	case hub.StatusWorking:
		return "●"
	default:
		return "○"
	}
}

// titleWidth is the row's title column, in display cells.
const titleWidth = 32

// navWidth is the nav pane's pinned width: cursor (2) + glyph, arbiter
// mark, and gap (3) + title column (titleWidth+1) + age ("12h34m"),
// plus 4 cells for the frame's borders and padding.
const navWidth = 48

// statWidth is what the optional stat column adds: the widest badge
// ("o4.8·xh"), the numeric form being narrower.
const statWidth = 7

// statColumn selects the optional right-hand column. Off by default so
// the nav keeps its pinned width and the poll skips reading transcripts.
type statColumn int

const (
	statColOff statColumn = iota
	statColContext
	statColModel
)

func (s statColumn) next() statColumn {
	if s == statColModel {
		return statColOff
	}
	return s + 1
}

// navCols is the nav pane's current width — navWidth, plus the stat
// column when one is showing.
func (m Model) navCols() int {
	if m.statCol == statColOff {
		return navWidth
	}
	return navWidth + statWidth
}

// statText is a pane's stat cell, blank when the transcript hasn't been
// read (or has nothing to say yet) — a blank column reads as "unknown",
// where "0k" would read as a measurement.
func statText(p hub.Pane, col statColumn) string {
	if p.Stats == nil {
		return ""
	}
	switch col {
	case statColContext:
		// Right-aligned: a column of numbers should line up on its
		// units. "1.3M" is the widest form formatTokens produces.
		if s := formatTokens(p.Stats.Context); s != "" {
			return fmt.Sprintf("%4s", s)
		}
		return ""
	case statColModel:
		badge := modelBadge(p.Stats.Model)
		if e := effortBadge(p.Stats.Effort); badge != "" && e != "" {
			badge += "·" + e
		}
		return badge
	}
	return ""
}

// modelBadge shortens a model id for the column: "claude-opus-4-8" reads
// "o4.8". Anything unrecognized falls back to its first id segment, so a
// model released after this code still shows something.
func modelBadge(model string) string {
	rest, ok := strings.CutPrefix(model, "claude-")
	if !ok {
		if model == "" {
			return ""
		}
		return firstSegment(model)
	}
	family, version, ok := strings.Cut(rest, "-")
	if !ok || family == "" {
		return firstSegment(rest)
	}
	// Dated ids ("haiku-4-5-20251001") carry a trailing date; the two
	// leading numbers are the version.
	parts := strings.Split(version, "-")
	num := parts[0]
	if len(parts) > 1 && len(parts[1]) < 4 {
		num += "." + parts[1]
	}
	return family[:1] + num
}

func firstSegment(s string) string {
	seg, _, _ := strings.Cut(s, "-")
	return seg
}

// effortBadge shortens the effort levels `claude --effort` accepts.
// Unknown values render blank rather than guessing.
func effortBadge(effort string) string {
	switch effort {
	case "low":
		return "lo"
	case "medium":
		return "md"
	case "high":
		return "hi"
	case "xhigh":
		return "xh"
	case "max":
		return "mx"
	}
	return ""
}

// formatTokens renders a token count in the narrowest honest form.
// Zero is blank: no reading, rather than a reading of nothing.
func formatTokens(n int) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return strconv.Itoa((n+500)/1000) + "k"
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

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
		return frame("new session", m.viewPicker(m.pickerBody()), m.pickerHints(),
			m.focused, m.width, m.height)
	}
	return frame("sessions", m.viewNav(), m.viewFooter(),
		m.focused, m.width, m.height)
}

// sessionCount is the header's count: the sessions being watched. The
// arbiter is coop's own machinery — like the hub panes the poll already
// filters out, it is not something you started and not something to
// count. Panes, not sessions, as the header has always counted.
func (m Model) sessionCount() int {
	n := len(m.panes)
	if _, ok := hub.FindArbiter(m.panes); ok {
		n--
	}
	return n
}

// arbiterDivider is the pinned arbiter section's heading, filled out to
// the nav's inner width: "─ arbiter ────…".
func arbiterDivider(w int) string {
	const head = "─ arbiter "
	if n := w - lipgloss.Width(head); n > 0 {
		return head + strings.Repeat("─", n)
	}
	return head
}

func (m Model) viewNav() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("coop") + "  " +
		footStyle.Render(fmt.Sprintf("%d sessions", m.sessionCount())) + "\n\n")

	if m.sessionCount() == 0 {
		b.WriteString("no sessions — start one by using the n key\n")
	}
	repo := ""
	for _, p := range m.panes {
		if p.Arbiter {
			// Pinned last by hub.SortPanes, under its own divider — it is
			// coop's own infrastructure, not one of the watched repos.
			b.WriteString("\n" + footStyle.Render(arbiterDivider(m.navCols()-4)) + "\n")
		} else if r := p.Repo(); r != repo {
			repo = r
			b.WriteString(titleStyle.Render(repo) + "\n")
		}
		st := statusStyle(p.Status)
		mark := " "
		title := cleanTitle(p.Title)
		if p.Arbiter {
			// Claude's derived title describes whatever the arbiter last
			// triaged, which reads as a work session in that repo. Its
			// mode is the only thing about it worth a row.
			title = "arbiter · " + hub.ArbiterModeOf(p)
		} else if p.ArbiterNote != "" {
			mark = "!" // arbiter escalated — detail line has the note
		}
		line := pad(st.Render(statusGlyph(p.Status)+mark+" "+
			truncate(title, titleWidth)), titleWidth+4) +
			age(p.Since())
		if m.statCol != statColOff {
			line = pad(line, navWidth-6) + footStyle.Render(statText(p, m.statCol))
		}
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

// pickerHeadLines is the prompt line plus the blank under it — the fixed
// top of the picker body, above both the list and the add row.
const pickerHeadLines = 2

// pickerRows is the picker body's geometry, shared by the draw and the
// click map so what you click is always what you see. Repos take two
// lines each (name, then dimmed path) and the add row is pinned to the
// bottom, so a list too long for the frame scrolls under it instead of
// pushing it out of reach.
type pickerRows struct {
	repos   []string // the visible slice of filteredRepos()
	start   int      // index in filteredRepos() of repos[0]
	note    string   // line drawn in place of an empty list; "" for none
	addLine int      // body line (0-based) the add row is drawn on
}

// pickerLayout lays the body out for avail lines of room.
func (m Model) pickerLayout(avail int) pickerRows {
	repos := m.filteredRepos()
	l := pickerRows{repos: repos}
	if len(repos) == 0 {
		// With no repos at all the head already says so; "no matches"
		// is only news when the filter is what emptied the list.
		if len(m.repos) > 0 {
			l.note = "no matches"
			l.addLine++
		}
		l.addLine += pickerHeadLines + 1
		return l
	}
	// The head, the blank above the add row and the add row itself are
	// fixed; the list gets whatever is left.
	fit := len(repos)
	if room := avail - pickerHeadLines - 2; room < 2*fit {
		fit = max(room/2, 0)
	}
	if fit < len(repos) {
		// The cursor rides the bottom visible row once it passes the
		// first page — keeps it on screen with no scroll offset to
		// carry in the Model.
		if cur := min(m.repoIdx, len(repos)-1); cur >= fit {
			l.start = cur - fit + 1
		}
	}
	l.repos = repos[l.start : l.start+fit]
	l.addLine = pickerHeadLines + 2*fit + 1
	return l
}

// pickerRowAt maps a terminal row to a picker index — a repo's index in
// filteredRepos(), or one past the end for the add row — and -1 for any
// other row. Like paneAt it walks the same layout the view draws.
func (m Model) pickerRowAt(y int) int {
	line := y - 1 // the frame's top border
	l := m.pickerLayout(m.pickerBody())
	if line == l.addLine {
		return len(m.filteredRepos())
	}
	if line < pickerHeadLines || l.note != "" {
		return -1
	}
	if i := (line - pickerHeadLines) / 2; i < len(l.repos) {
		return l.start + i
	}
	return -1
}

// pickerBody is how many lines frame() will leave the picker's body:
// the height, less the border and the hint block under it.
func (m Model) pickerBody() int {
	hints := strings.Count(strings.TrimRight(m.pickerHints(), "\n"), "\n") + 1
	return max(m.height-2-hints, 0)
}

func (m Model) pickerHints() string {
	if m.adding {
		return flowHints([]string{"enter add & start", "esc cancel"}, m.width-4)
	}
	esc := "esc cancel"
	if m.repoFilter != "" {
		esc = "esc clear"
	}
	enter := "enter create"
	if m.repoIdx >= len(m.filteredRepos()) {
		enter = "enter add repo"
	}
	return flowHints([]string{"type to filter", "↑/↓ select", enter, esc},
		m.width-4)
}

func (m Model) viewPicker(avail int) string {
	var b strings.Builder
	if m.adding {
		b.WriteString(footStyle.Render("add repo: ") + m.repoPath + "▌\n\n")
		b.WriteString(footStyle.Render("~/src/repo or /abs/path") + "\n")
		return b.String()
	}
	switch {
	case m.repoFilter != "":
		b.WriteString(footStyle.Render("filter: ") + m.repoFilter + "▌\n\n")
	case len(m.repos) == 0:
		b.WriteString(footStyle.Render("no repos configured yet") + "\n\n")
	default:
		b.WriteString(footStyle.Render("pick a repo — type to filter") + "\n\n")
	}
	l := m.pickerLayout(avail)
	if l.note != "" {
		b.WriteString(footStyle.Render(l.note) + "\n")
	}
	row := func(text string, selected bool) {
		if selected {
			b.WriteString(cursorStyle.Render("▸ ") + text + "\n")
		} else {
			b.WriteString("  " + text + "\n")
		}
	}
	for i, r := range l.repos {
		row(titleStyle.Render(filepath.Base(r)), l.start+i == m.repoIdx)
		b.WriteString("  " + footStyle.Render(tildify(r)) + "\n")
	}
	b.WriteString("\n")
	row(titleStyle.Render("+ add new repo"), m.repoIdx >= len(m.filteredRepos()))
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
	inner := m.width - 4 // frame() draws "│ " and " │"
	box, overflow := m.viewMsgBox(inner)

	hints := []string{"↑/↓ select", "enter focus", "n new", m.arbiterHint(), "? help"}
	switch {
	case m.showHelp:
		hints = []string{"↑/↓ select", "enter focus", "tab next input",
			"shift+←/→ switch pane", "0-9 answer", "space apply suggestion",
			"bksp erase", "/ command", "n new", "x kill", "s stats", m.arbiterHint(),
			"pgup/pgdn scroll msg", "q quit", "? close"}
	default:
		// Contextual chips, most actionable first — rows are scarce at
		// navWidth, so neither is worth a permanent slot.
		if overflow {
			hints = append([]string{"pgup/pgdn scroll"}, hints...)
		}
		if s := m.selectedSuggest(); s != "" {
			hints = append([]string{"space apply " + s}, hints...)
		}
	}
	foot := flowHints(hints, inner)
	if box != "" {
		foot = box + "\n\n" + foot // blank row sets the message off the hints
	}
	return foot
}

// viewMsgBox renders the message wrapped to width, windowed to the visible
// line cap, and reports whether there is more of it than fits.
func (m Model) viewMsgBox(width int) (string, bool) {
	text, style := m.message()
	if text == "" {
		return "", false
	}
	lines, total := wrapMsg(text, width, m.msgBoxMax(), m.msgScroll)
	for i, l := range lines {
		lines[i] = style.Render(l)
	}
	return strings.Join(lines, "\n"), total > len(lines)
}

// msgBoxMax is how many message lines the box shows at once. It gives up
// height on a short terminal rather than squeezing the session list away.
func (m Model) msgBoxMax() int {
	return min(msgBoxLines, max(1, m.height/4))
}

// msgBoxLines is the message box's visible line cap on a roomy terminal.
const msgBoxLines = 4

// maxMsgScroll is the last offset that still fills the box; 0 when the
// whole message already fits.
func (m Model) maxMsgScroll() int {
	if m.msg == "" {
		return 0
	}
	_, total := wrapMsg(m.msg, m.width-4, m.msgBoxMax(), 0)
	return max(0, total-m.msgBoxMax())
}

// wrapMsg word-wraps text to width and returns the window of at most max
// lines starting at offset, along with the total wrapped line count. The
// offset is clamped, so a stale scroll position can never blank the box.
func wrapMsg(text string, width, max, offset int) ([]string, int) {
	if width < 1 {
		width = 1
	}
	// reflow's wordwrap breaks on '-' by default, which splits an ordinary
	// hyphenated word ("don't-ask-again") across lines and, once the
	// overhang meets the hard wrap below, mid-syllable. Whitespace is the
	// only breakpoint worth having here.
	w := wordwrap.NewWriter(width)
	w.Breakpoints = nil
	w.Write([]byte(text)) // buffered in memory; cannot fail
	w.Close()
	// wordwrap leaves a word longer than width overhanging, which frame()
	// would then clip; wrap hard-breaks whatever is still too wide.
	lines := strings.Split(wrap.String(w.String(), width), "\n")
	if offset > len(lines)-max {
		offset = len(lines) - max
	}
	if offset < 0 {
		offset = 0
	}
	end := min(offset+max, len(lines))
	return lines[offset:end], len(lines)
}

// arbiterHint is the a key's footer chip, doubling as the mode display:
// "a arbiter off|recommend|full".
func (m Model) arbiterHint() string {
	arb, ok := hub.FindArbiter(m.panes)
	if !ok {
		return "a arbiter off"
	}
	return "a arbiter " + hub.ArbiterModeOf(arb)
}

// selectedSuggest is the digit the space key would apply for the
// selected row: the arbiter's suggestion, or "" when it has none.
func (m Model) selectedSuggest() string {
	i := m.indexOf(m.selectedID)
	if i < 0 {
		return ""
	}
	return hub.ArbiterSuggestOf(m.panes[i])
}

// arbiterDetail is the selected row's arbiter line: an active
// escalation note, else the last answer, else nothing. The text comes back
// unstyled so it can be wrapped before the style is applied per line.
func (m Model) arbiterDetail() (string, lipgloss.Style) {
	i := m.indexOf(m.selectedID)
	if i < 0 {
		return "", footStyle
	}
	p := m.panes[i]
	if p.ArbiterNote != "" {
		return "arbiter: " + p.ArbiterNote, needsStyle
	}
	if last, ok := hub.ParseArbiterLast(p.ArbiterLast); ok {
		return fmt.Sprintf("answered %s by arbiter %s ago — %s",
			last.Digit, age(last.At), last.Reason), footStyle
	}
	return "", footStyle
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
