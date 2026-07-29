// coop: a tmux-native TUI for Claude Code sessions.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"coop/internal/config"
	"coop/internal/hub"
	"coop/internal/tui"
)

// hubBase seeds hub session names: "roost", then "roost-2", "roost-3",
// … so several hub instances (one per monitor, say) can share one socket.
const hubBase = "roost"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitCmds splits a comma-separated list of pane_current_command values,
// trimming whitespace and dropping empty entries.
func splitCmds(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// loadTmuxOverrides reads config.json's "tmux" list, split into argv
// words. A missing config is fine (no overrides); a malformed config or
// command is an error — silently dropping an override the user wrote
// would be worse than refusing to start.
func loadTmuxOverrides(cfgPath string) ([][]string, error) {
	c, err := config.Load(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var overrides [][]string
	for i, line := range c.Tmux {
		words, err := config.SplitCommand(line)
		if err != nil {
			return nil, fmt.Errorf("%s: tmux[%d]: %w", cfgPath, i, err)
		}
		overrides = append(overrides, words)
	}
	return overrides, nil
}

// tmuxDefaults is the server setup once kept in a tmux.conf, as chained
// commands: hub plumbing (monitor-bell latches needs-input, status off
// keeps the live pane clean) plus terminal QoL (true color, snappy ESC,
// mouse, titles, clipboard).
// Every command must be idempotent — the chain re-runs whenever a hub
// launches against an already-running server (so plain "set", never
// "set -a": appending flags would accumulate per launch).
func tmuxDefaults() [][]string {
	return [][]string{
		{"set", "-g", "default-terminal", "tmux-256color"},
		{"set", "-g", "terminal-overrides", ",xterm-ghostty:RGB,*:RGB"},
		{"set", "-s", "escape-time", "10"},
		{"set", "-g", "mouse", "on"},
		{"set", "-g", "history-limit", "50000"},
		{"set", "-g", "allow-passthrough", "on"},
		{"set", "-g", "set-titles", "on"},
		{"set", "-g", "set-titles-string", "#S — #T"},
		{"set", "-g", "set-clipboard", "on"},
		{"set", "-g", "focus-events", "on"},
		{"set", "-wg", "monitor-bell", "on"},
		{"set", "-g", "status", "off"},
		{"bind", "-n", "S-Left", "select-pane", "-L"},
		{"bind", "-n", "S-Right", "select-pane", "-R"},
	}
}

// createArgv builds the tmux argv creating a detached session named
// name hosting a fresh coop, marked @coop in the same invocation so
// no other hub's poll ever sees it unmarked. tmuxDefaults and then
// overrides (config.json "tmux", split into words — last wins) are
// chained before new-session so the hub pane itself is born under them;
// -f /dev/null keeps the user's personal tmux.conf off this socket.
func createArgv(self, socket, cmds, cfgPath, claudeCmd, doneTTL string, overrides [][]string, name string) []string {
	argv := []string{"tmux", "-L", socket, "-f", os.DevNull, "start-server"}
	for _, c := range tmuxDefaults() {
		argv = append(append(argv, ";"), c...)
	}
	for _, c := range overrides {
		argv = append(append(argv, ";"), c...)
	}
	return append(argv, ";", "new-session", "-d", "-s", name,
		self, "-socket", socket, "-allowed-cmds", cmds,
		"-config", cfgPath, "-claude-cmd", claudeCmd, "-done-ttl", doneTTL,
		";", "set-option", "-t", name+":", hub.HubMarker, "1")
}

// attachArgv builds the tmux argv attaching this terminal to an
// existing hub session ("=" — exact name match).
func attachArgv(socket, name string) []string {
	return []string{"tmux", "-L", socket, "attach-session", "-t", "=" + name}
}

// pickDetachedHub returns a hub session with no client attached — an
// orphan whose terminal closed — or "" if every hub is in use.
func pickDetachedHub(sessions []hub.SessionInfo) string {
	for _, s := range sessions {
		if s.Hub && !s.Attached {
			return s.Name
		}
	}
	return ""
}

// nextHubName picks the first free hub-N name. Every session name
// counts as taken, hub or not — a repo directory named "roost-2" claims
// that session name the same way another hub instance does.
func nextHubName(sessions []hub.SessionInfo) string {
	var names []string
	for _, s := range sessions {
		names = append(names, s.Name)
	}
	return hub.NextSessionName(names, hubBase)
}

// launchIntoTmux puts this terminal into a hub session and never
// returns on success: reattach a detached hub if one exists, else
// create a fresh one (retrying past name races) and attach to it.
func launchIntoTmux(tm *hub.ExecTmux, socket, cmds, cfgPath, claudeCmd, doneTTL string, overrides [][]string) error {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH: %w", err)
	}
	sessions, err := tm.Sessions()
	if err != nil {
		return err
	}
	name := pickDetachedHub(sessions)
	if name == "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		// Two launches can race for the same name; the loser's
		// new-session fails with "duplicate session" — re-list and retry.
		for tries := 0; ; tries++ {
			name = nextHubName(sessions)
			argv := createArgv(self, socket, cmds, cfgPath, claudeCmd,
				doneTTL, overrides, name)
			out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
			if err == nil {
				break
			}
			if tries >= 5 || !strings.Contains(string(out), "duplicate session") {
				return fmt.Errorf("tmux new-session: %w: %s", err,
					strings.TrimSpace(string(out)))
			}
			if sessions, err = tm.Sessions(); err != nil {
				return err
			}
		}
	}
	return syscall.Exec(tmuxBin, attachArgv(socket, name), os.Environ())
}

func main() {
	// Helper subcommands (the arbiter's tools) bypass the TUI entirely.
	if isArbiterCmd(os.Args[1:]) {
		os.Exit(runArbiterCLI(os.Args[1:], os.Stdout, os.Stderr))
	}

	socket := flag.String("socket", envOr("COOP_SOCKET", "coop"),
		"tmux socket name (tmux -L)")
	cmds := flag.String("allowed-cmds", envOr("COOP_ALLOWED_CMDS", "claude,node"),
		"comma-separated pane_current_command values digit answers may target")
	configPath := flag.String("config", envOr("COOP_CONFIG", config.DefaultPath()),
		"config.json listing repos for the new-session picker")
	claudeCmd := flag.String("claude-cmd", envOr("COOP_CLAUDE_CMD", "claude"),
		"command run in sessions created from the picker")
	doneTTL := flag.String("done-ttl", envOr("COOP_DONE_TTL", "5m"),
		"how long a finished session shows done before decaying to idle (0 disables)")
	flag.Parse()

	ttl, err := time.ParseDuration(*doneTTL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "coop: -done-ttl:", err)
		os.Exit(1)
	}

	tm := &hub.ExecTmux{Socket: *socket}
	if os.Getenv("TMUX") == "" {
		overrides, err := loadTmuxOverrides(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "coop:", err)
			os.Exit(1)
		}
		// launchIntoTmux ends in exec on success — reaching here is failure.
		err = launchIntoTmux(tm, *socket, *cmds, *configPath, *claudeCmd,
			*doneTTL, overrides)
		fmt.Fprintln(os.Stderr, "coop:", err)
		os.Exit(1)
	}

	// Which hub session is ours — "roost", "roost-2", … depending on launch.
	hubSession, err := tm.SessionForPane(os.Getenv("TMUX_PANE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "coop: own session:", err)
		os.Exit(1)
	}
	// Idempotent re-mark: creation chains it, but a hand-started coop
	// inside a plain tmux session on this socket must mark itself too.
	if err := tm.SetSessionOption(hubSession, hub.HubMarker, "1"); err != nil {
		fmt.Fprintln(os.Stderr, "coop: mark:", err)
	}
	// Cosmetic only — a failed style setup must not stop the hub.
	if err := hub.ApplyHubStyle(tm, hubSession, os.Getenv("TMUX_PANE")); err != nil {
		fmt.Fprintln(os.Stderr, "coop: style:", err)
	}
	// A missing config is not an error the picker should refuse to open
	// on — its add row is how the first repo gets written. A malformed
	// one is, since adding to it would mean rewriting it.
	loadRepos := func() ([]string, error) {
		c, err := config.Load(*configPath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return c.Repos, nil
	}
	addRepo := func(repo string) (string, error) {
		return config.AddRepo(*configPath, repo)
	}
	var arbCfg tui.ArbiterConfig
	// Only derive a config dir from an actual config path — "" would
	// resolve to "." via filepath.Dir and seed an arbiter/ in the cwd
	// instead of leaving the TUI's "no config dir" guard to fire.
	if *configPath != "" {
		arbCfg.ConfigDir = filepath.Dir(*configPath)
	}
	if c, err := config.Load(*configPath); err == nil {
		arbCfg.Model = c.Arbiter.Model
	}
	m := tui.New(tm, splitCmds(*cmds), hubSession, *socket,
		os.Getenv("TMUX_PANE"), *claudeCmd, loadRepos, addRepo, ttl, arbCfg)
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithReportFocus(),
		tea.WithMouseCellMotion()).Run()
	if fm, ok := final.(tui.Model); ok {
		id := fm.LivePane()
		if id == "" {
			// A split may have been in flight when the TUI exited, so
			// livePane never made it into the final model. Fall back to
			// looking up the marked pane directly; best-effort either way.
			id, _ = tm.FindMarkedPane(hubSession, hub.LiveMarker)
		}
		if id != "" {
			tm.KillPane(id) // best-effort; pane may already be gone
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "coop:", err)
		os.Exit(1)
	}
}
