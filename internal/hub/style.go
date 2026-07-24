// Style: the hub window's two-panel look. The nav's lipgloss frame
// (internal/tui) and the tmux pane borders share this palette so
// whichever panel holds focus lights up in the same colour.
package hub

// Focus palette, as 256-colour indexes: amber for the focused panel,
// gray for the rest. TitleText keeps the preview's title bar readable
// even when the border line around it is drawn in BorderDim.
const (
	FocusAccent = "179"
	BorderDim   = "238"
	TitleText   = "250"
)

// ApplyHubStyle styles the hub window for the two-panel look: a tmux
// title bar per pane, and focus events so the TUI can recolour its own
// frame the moment focus moves. navPane's bar is blanked — the TUI
// embeds its title in the lipgloss frame.
//
// pane-active-border-style starts dim, matching the nav holding focus
// at startup: tmux would otherwise light every border segment touching
// the active pane, painting a stray amber line over the nav's blanked
// bar. It is window-scoped (a per-pane set silently lands on the
// window), so the TUI flips it amber/dim on focus events instead —
// amber exactly while the preview pane is the active one.
// Cosmetic setup: callers may treat a failure as non-fatal.
func ApplyHubStyle(tm Tmux, hubSession, navPane string) error {
	if err := tm.SetServerOption("focus-events", "on"); err != nil {
		return err
	}
	for _, o := range [][2]string{
		{"pane-border-status", "top"},
		{"pane-border-style", "fg=colour" + BorderDim},
		{"pane-active-border-style", "fg=colour" + BorderDim},
		// The default 'colour' indicator paints only half of a border
		// line shared by two panes to point at the active one — the
		// split would read as broken, half-lit. Off keeps whole lines
		// in one style.
		{"pane-border-indicators", "off"},
	} {
		if err := tm.SetWindowOption(hubSession, o[0], o[1]); err != nil {
			return err
		}
	}
	if navPane == "" {
		return nil
	}
	return tm.SetPaneOption(navPane, "pane-border-format", "")
}
