package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"coop/internal/hub"
	"coop/internal/toolbox"
)

// isHookCmd reports whether argv selects the hook subcommand — the
// status publisher Claude Code invokes (via the injected settings file)
// on every hook event.
func isHookCmd(args []string) bool {
	return len(args) > 0 && args[0] == "hook"
}

// hookSocket resolves the socket for this hook invocation from $TMUX
// alone — deliberately not cliSocket, and deliberately ignoring
// $COOP_SOCKET. cliSocket lets COOP_SOCKET win because coop peek is run
// by a human who names the hub's socket explicitly, possibly from outside
// any tmux pane at all; the hook has no such freedom. It runs inside the
// exact pane it's about to mark, so the only socket that can be right is
// the one that pane actually lives on. Honoring an exported COOP_SOCKET here
// would have the hook write status onto a different tmux server — pane
// ids are per-server, so the write would silently mark nothing, or
// worse, some unrelated pane that happens to share the id.
func hookSocket(tmuxEnv string) string {
	if path, _, ok := strings.Cut(tmuxEnv, ","); ok && path != "" {
		return filepath.Base(path)
	}
	return "coop"
}

// runHookCLI publishes one hook event onto this pane's user options.
// It always returns 0, and prints nothing except the one thing below:
// hook stdout is parsed by Claude Code as JSON and stderr lands in the
// session's transcript, so every failure here is a silent no-op — the
// pane just stays on the title fallback until the next event.
func runHookCLI(stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	p, ok := hub.ParseHookPayload(stdin)
	if !ok {
		return 0
	}
	// Before the pane gate, because it is not about the pane: a session's
	// toolbox is a property of its PATH, not of tmux.
	emitSessionNote(stdout, p, getenv)
	pane := getenv("TMUX_PANE")
	tmuxEnv := getenv("TMUX")
	if pane == "" || tmuxEnv == "" {
		return 0 // not in a tmux pane coop could mark
	}
	tm := &hub.ExecTmux{Socket: hookSocket(tmuxEnv)}
	// Best-effort: a failed write is one stale status; the next event
	// or the SessionEnd unset corrects it.
	_ = hub.ApplyHook(tm, pane, p, time.Now())
	// One headless judge per needs-input episode, spawned detached right
	// after the status publish above.
	spawnJudge(tm, hookSocket(tmuxEnv), pane, p)
	return 0
}

// sessionNote resolves what this session should be told about its own
// toolbox. A var for the reason judgeSpawn and claimEpisode are: what is
// worth testing here is the wiring — which event prints, in what
// envelope — and doing that against the real one would mean standing up a
// state tree under the developer's own home. What the note *says* is
// toolbox's to test, where the state root is redirectable.
var sessionNote = func(getenv func(string) string) string {
	return toolbox.SessionNote(toolbox.ShimDirOn(getenv("PATH")))
}

// hookOutput is Claude Code's SessionStart hook response: the string in
// additionalContext is appended to the session's context.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// emitSessionNote is the one thing coop hook prints, and the exception to
// the silence the rest of this file keeps. It tells a session that its
// commands are containerized — which nothing else does, because a shim is
// deliberately invisible: claude types "go build" and the permission
// layer sees exactly that. Transparency is right for the common path and
// wrong at the moment something fails, where docker's bare "executable
// file not found" reads as a coop bug and a `go install` disappears into
// a $HOME nobody looks in.
//
// SessionStart rather than a CLAUDE.md, and the difference is the point.
// The only files Claude Code discovers are the repo's (committed, so it
// reaches sessions coop did not launch, where no shim is on PATH and the
// note is a lie — the same leak that put the toolbox grants in the
// injected --settings rather than in .claude/settings.json) and the
// user's global one (every project on the machine). This reaches exactly
// the sessions coop launched, carries nothing on disk, and is rebuilt
// from the running binary every time.
//
// Every source, deliberately including compact: that event is why this
// beats both a CLAUDE.md and --append-system-prompt, which are read once
// at launch. The failure being guarded is a model that has forgotten the
// toolchain is containerized 200k tokens in, so coming back after each
// compaction is the mechanism rather than a bonus.
//
// Silent on every failure path, like everything else here: a session
// missing this note behaves exactly as every session did before it
// existed.
func emitSessionNote(w io.Writer, p hub.HookPayload, getenv func(string) string) {
	if w == nil || p.Event != "SessionStart" {
		return
	}
	note := sessionNote(getenv)
	if note == "" {
		return
	}
	var out hookOutput
	out.HookSpecificOutput.HookEventName = p.Event
	out.HookSpecificOutput.AdditionalContext = note
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	w.Write(b) // best-effort: unreadable stdout is not worth a word anywhere
}

// judgeTmux is the slice of hub.Tmux the spawn gate needs. Narrow so the
// gate tests can drive it without doubling the whole tmux surface — the
// gates are where the invariants live, and they must be testable without
// starting a real child.
type judgeTmux interface {
	GlobalOption(name string) (string, error)
	PaneOption(pane, name string) (string, error)
}

// spawnJudge starts a detached judge for this pane. It runs inside coop
// hook, so the event itself is the trigger — no leader election, no hub
// required, and judging keeps working with every TUI detached.
//
// Everything here is best-effort and silent: a miss is one row that reads
// plain "waiting", which the operator's own eyes cover.
func spawnJudge(tm judgeTmux, socket, pane string, p hub.HookPayload) {
	if status, _, _ := hub.HookStatus(p); status != "waiting" {
		return
	}
	if hub.ArbiterMode(tm) == hub.ArbiterModeOff {
		return
	}
	// The episode key: one judge per needs-input episode, claimed
	// atomically below. Two hook processes can be in flight for the same
	// dialog (a PermissionRequest and the Notification about it), and two
	// judges on one dialog means two model calls for it and two notes
	// overwriting each other on the row.
	since, err := tm.PaneOption(pane, hub.ClaudeSinceMarker)
	if err != nil || since == "" {
		// No episode key: ApplyHook's write just failed, or this pane
		// publishes nothing. Skipping is the safe direction — keying the
		// claim on the pane alone would wedge it until the claim aged out.
		return
	}
	// Everything that can fail without a judge running comes first, so
	// the claim is taken last: a claim consumed by a failed spawn would
	// wedge this dialog forever, since the next hook event for it carries
	// the same since. The spawn itself is the one failure left after the
	// claim, and it releases.
	self, err := os.Executable()
	if err != nil {
		return
	}
	// NudgeDetail stays the tool call and nothing else — the judge prompt
	// renders it after "trigger:", where everything to the end of the
	// line is the monitored session's own text. The subagent rides in its
	// own argument instead of being glued on, so that boundary keeps
	// meaning exactly one thing.
	argv := judgeArgv(self, socket, pane, hub.NudgeDetail(p), hub.HookAgent(p))
	if !claimEpisode(pane, since) {
		return
	}
	// The judge gets a constructed environment, not this one: this
	// process's environment is the judged session's (see hub.JudgeEnv).
	if err := judgeSpawn(argv, hub.JudgeEnv(os.Environ())); err != nil {
		releaseEpisode(pane, since)
	}
}

// judgeSpawn is the spawn itself, a var so the gates above can be tested
// without starting a real judge. Nothing but tests replaces it.
var judgeSpawn = startJudge

// claimEpisode and releaseEpisode are vars for the same reason, and one
// more: the claims directory deliberately ignores $XDG_STATE_HOME (see
// hub.claimRoot — a monitored session can set that variable, so honouring
// it here would hand the session the dedupe). A test therefore cannot
// point the real ones at a t.TempDir() with the environment, and must not
// be allowed to write into the developer's own home instead.
var (
	claimEpisode   = hub.ClaimEpisode
	releaseEpisode = hub.ReleaseEpisode
)

// startJudge runs argv detached with exactly env. The child is setsid'd
// because Claude Code reaps the hook's process group the moment the hook
// returns, and the judge outlives it by a model turn.
func startJudge(argv, env []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detached: never waited on, so it is reparented to init when this
	// process exits a few microseconds from now.
	return cmd.Process.Release()
}
