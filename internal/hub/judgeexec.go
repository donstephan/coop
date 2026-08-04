package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// DefaultArbiterModel is what arbiter.model defaults to. It was haiku
// while a verdict could send a keystroke and the classification was the
// product. It is not any more: the arbiter only annotates, so the note a
// human reads on the row — and the digit they apply with one key — *is*
// the product, formed by summarizing a captured terminal that an
// attacker can influence. The whole per-episode cost is a bounded prompt
// and a one-line verdict, so the better model is worth it; an operator
// who disagrees sets arbiter.model.
const DefaultArbiterModel = "sonnet"

// judgeTimeout bounds one episode. A judge that hangs is one row that
// stays plain "waiting", which the operator's own eyes still cover.
const judgeTimeout = 60 * time.Second

// judgeEnvKeep is the *only* inherited environment a judge gets. An
// allowlist rather than a denylist because the environment the judge
// inherits is the monitored session's own — coop hook runs in its pane —
// and a denylist of "variables Claude Code honours" is a list that grows
// with every Claude Code release, silently, in the attacker's favour.
// PATH alone decides which binary *is* the judge; CLAUDE_CONFIG_DIR,
// HOME, the proxy pair, NODE_EXTRA_CA_CERTS and the Bedrock/Vertex
// switches each decide where its prompt goes or what credentials it
// carries. None of them are here, and neither is anything else a future
// release adds, because nothing is here that is not named below.
//
// What is left through carries no authority: text decoding and
// timestamps, plus TMUX_TMPDIR — which the judge needs to find the same
// tmux server the hook just wrote to, and whose worst case is a session
// pointing the judge at a server holding only that session's own fake
// panes, while dropping it would break every operator who sets it.
//
// This raises the bar; it does not seal it. coop and the sessions it
// watches run as the same user, so a session that already has code
// execution can write ~/.claude, the config file, or the judge binary
// itself. See the arbiter section of CLAUDE.md.
var judgeEnvKeep = []string{"LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMUX_TMPDIR"}

// judgeSystemPath is the constructed PATH's fixed tail: the standard
// system directories, in the order a login shell would search them.
var judgeSystemPath = []string{"/usr/local/bin", "/usr/bin", "/bin"}

// claimTTL bounds how long an episode claim file is honoured. The key
// already names the episode, so a stale claim only ever blocks an
// episode that is over; the TTL is about not accumulating one file per
// dialog forever.
const claimTTL = 24 * time.Hour

// ClaimEpisode claims one needs-input episode, so exactly one judge is
// spawned for it. Two coop hook processes can be in flight for the same
// dialog (a PermissionRequest and the Notification about it), and without
// the claim each starts a judge: two model calls billed for one dialog,
// and two notes racing to land on the row, where the last write wins and
// the operator has no way to tell which verdict they are reading. One
// episode is one turn and one note. A tmux option cannot be tested and
// set in one step, so the claim is an O_EXCL create under coop's state
// directory, which can.
//
// The key is the pane plus the episode's @coop_status_since — an option
// that only moves when the status actually changes, so the next dialog on
// the same pane usually gets a different key and claims cleanly even if
// this file is never removed (a killed judge, a reboot). "Usually":
// since is unix seconds, so two episodes on one pane inside a single
// second share a key and the second is silently never judged. That fails
// safe — a row that just reads "waiting", which is true, and which the
// operator's own eyes still cover.
//
// The directory comes from claimRoot, not stateDir, because this runs
// inside coop hook — see claimRoot for why that distinction is the whole
// point.
//
// false means someone else holds it or there is nowhere to write: not
// judging is the safe direction.
func ClaimEpisode(pane, since string) bool {
	dir := claimRoot()
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	pruneClaims(dir, time.Now())
	f, err := os.OpenFile(filepath.Join(dir, claimName(pane, since)),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// ReleaseEpisode undoes a claim whose judge never started. Without it a
// claim is consumed by a spawn that failed, and since the next hook
// event for the same dialog carries the same since — the option only
// moves on a status change — that dialog would never be judged at all.
// Best-effort: a claim left behind is one unjudged episode, which is
// what the failed spawn already cost.
func ReleaseEpisode(pane, since string) {
	dir := claimRoot()
	if dir == "" {
		return
	}
	os.Remove(filepath.Join(dir, claimName(pane, since)))
}

// claimRoot resolves the claims directory without consulting the
// environment — the one state path that must not. Every other one goes
// through stateDir and honours $XDG_STATE_HOME, which is right for a
// path the operator's own shell sets; but ClaimEpisode runs inside coop
// hook, in the monitored pane, where that variable is the session's to
// set. It does not even take code execution to set it: an env block in a
// repo's .claude/settings.json reaches claude's environment on startup.
// A session that varies it between two hook events lands each claim in a
// fresh directory, so every claim succeeds and one dialog gets as many
// judges as it has events — the exact race the O_EXCL create exists to
// stop. The password database is the one home directory a monitored
// session cannot rewrite, and it is already what JudgeEnv hands the
// judge, so claims, audit log and judge log now all resolve the same way.
func claimRoot() string {
	home := claimHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "coop", "claims")
}

// claimHome is judgeHome, indirected only so tests can point the claims
// directory at a t.TempDir(). A test cannot redirect it with an
// environment variable, because not being redirectable by one is the
// entire property claimRoot exists to have.
var claimHome = judgeHome

// claimName renders one claim file name. Pane ids are "%12" and since is
// unix seconds, but both arrive from tmux options, so everything outside
// [A-Za-z0-9] folds to "-" and the name stays a single path element
// whatever a future version writes there.
func claimName(pane, since string) string {
	fold := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}
	return strings.Map(fold, pane) + "-" + strings.Map(fold, since)
}

// pruneClaims drops claims older than claimTTL. Best-effort and cheap:
// one readdir per needs-input episode over a directory holding one empty
// file per episode of the last day.
func pruneClaims(dir string, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < claimTTL {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
}

// JudgeSystemPrompt is the judge's --append-system-prompt: the fixed
// role prompt plus the user's policy.
func JudgeSystemPrompt(policy string) string {
	return arbiterPreamble + policy
}

// LoadArbiterPolicy reads configDir/arbiter.md, seeding it first if it
// does not exist, and returns it alongside nothing else — the caller
// gets the working directory from ArbiterHome.
func LoadArbiterPolicy(configDir string) (string, error) {
	if _, err := ArbiterHome(configDir); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(configDir, "arbiter.md"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ClaudeJudgeRunner returns the Run func for a real episode: one headless
// claude, prompt on stdin, JSON envelope on stdout.
//
// claude comes from the constructed PATH rather than the hub's
// -claude-cmd: that override carries flags meant for an interactive
// session, and --continue or --resume on a one-shot -p run would answer
// from an unrelated conversation.
func ClaudeJudgeRunner(dir, model, systemPrompt string) func(string) (string, error) {
	return func(prompt string) (string, error) {
		bin, err := lookJudgeClaude()
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), judgeTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, judgeClaudeArgs(model, systemPrompt)...)
		cmd.Dir = dir
		cmd.Env = JudgeEnv(os.Environ())
		cmd.Stdin = strings.NewReader(prompt)
		out, err := cmd.Output()
		if err != nil {
			return "", judgeRunError(err)
		}
		return string(out), nil
	}
}

// judgeRunError folds a failed claude run's stderr into the error.
// cmd.Output parks it in (*exec.ExitError).Stderr, where nothing read it
// — so a claude that refused to start (bad model id, no credentials)
// logged "exit status 1" and nothing more, and the judge is detached
// with both its own streams on /dev/null, making judge.log the only
// channel it has. sanitizeNote does the bounding: one line, no control
// bytes (the log gets read with cat, and an ANSI escape from a failing
// child is a terminal-injection vector) and capped, so a node stack
// trace cannot append a screenful per episode.
func judgeRunError(err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	msg := sanitizeNote(string(ee.Stderr))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

// judgeClaudeArgs is one episode's claude command line, kept apart from
// the spawn so the restrictions on it can be asserted without running
// claude.
//
// The prompt body is screen text from a session that may itself be under
// injection, so "the judge runs no tools" is enforced rather than
// assumed: --tools "" is the built-in tool set, empty, and
// --strict-mcp-config keeps any configured MCP server's tools out. Both
// are positive restrictions on the command line — permissions.allow in
// the operator's own settings.json cannot widen a tool set that is empty,
// which is why this is not left to a settings file. And no --settings, so
// none of coop's hooks are registered for the judge itself: a judge whose
// hooks fired would write status onto panes.
func judgeClaudeArgs(model, systemPrompt string) []string {
	return []string{"-p",
		"--model", model,
		"--append-system-prompt", systemPrompt,
		"--tools", "",
		"--strict-mcp-config",
		"--output-format", "json"}
}

// JudgeEnv builds the environment a judge runs under: judgeEnvKeep's
// pass-throughs, plus a HOME and a PATH constructed here rather than
// inherited. Both hops out of the pane use it — coop hook's spawn of the
// judge and the judge's spawn of claude — because either alone would
// leave the judged session's values one process away from the thing they
// decide.
func JudgeEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(judgeEnvKeep, name) {
			out = append(out, kv)
		}
	}
	if home := judgeHome(); home != "" {
		out = append(out, "HOME="+home)
	}
	return append(out, "PATH="+judgePath())
}

// judgeHome is the user's home directory from the password database, not
// from $HOME: HOME is where claude finds its settings, its hooks and its
// credentials, and the process that hands it over is running in a pane
// the judged session controls. os.UserHomeDir is the fallback and it
// *is* $HOME — a judge with no home at all cannot run, so a passwd
// lookup that fails buys back liveness at the cost of this guarantee.
func judgeHome() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// judgePath is the PATH the judge and its claude resolve binaries
// through. Inheriting it would mean one prepended directory in the
// judged session's environment makes an arbitrary binary *be* the judge:
// arbitrary code as the operator, and any verdict it likes.
//
// The directory of coop's own executable leads, because claude is
// normally installed alongside coop (both land in ~/.local/bin), and a
// judge that cannot find claude judges nothing at all. That is the
// honest limit of this: the trust is in "wherever coop itself was
// installed from", not in a signature — anyone who can replace the
// coop binary already owns the judge outright.
func judgePath() string {
	var dirs []string
	add := func(d string) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	if home := judgeHome(); home != "" {
		add(filepath.Join(home, ".local", "bin"))
	}
	for _, d := range judgeSystemPath {
		add(d)
	}
	return strings.Join(dirs, ":")
}

// lookJudgeClaude resolves claude against judgePath rather than the
// process's own PATH. exec.Command resolves the name through
// os.Getenv("PATH") — cmd.Env does not affect the lookup — so without
// this the constructed PATH would decide nothing whenever a judge is
// started by hand or by a coop that did not scrub.
func lookJudgeClaude() (string, error) {
	for _, dir := range strings.Split(judgePath(), ":") {
		p := filepath.Join(dir, "claude")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("claude not found in %s", judgePath())
}
