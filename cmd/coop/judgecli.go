package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"coop/internal/config"
	"coop/internal/hub"
)

// isJudgeCmd reports whether argv selects the judge subcommand — one
// headless triage episode, spawned detached by coop hook. Dispatched
// before flag parsing for the same reason coop hook is: a subcommand
// that is another coop process calling in must not pay for a TUI it will
// never draw.
func isJudgeCmd(args []string) bool {
	return len(args) > 0 && args[0] == "judge"
}

// judgeArgv is the command line coop hook spawns. Kept apart from the
// spawn itself so the shape is testable without starting a process.
//
// agent carries the one fact the judge cannot recover on its own: that
// the dialog belongs to a subagent. Only the hook payload says so, and
// the transcript — whose format is documented as internal and unstable —
// is not a second source worth depending on.
func judgeArgv(self, socket, pane, detail, agent string) []string {
	return []string{self, "judge", "-socket", socket,
		"-detail", detail, "-agent", agent, pane}
}

// runJudgeCLI runs one triage episode and returns an exit code nobody
// reads — the process is detached, so its only outputs are the tmux
// options Judge writes and the log. Failures are logged, never surfaced.
func runJudgeCLI(args []string) int {
	fs := flag.NewFlagSet("coop judge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "coop", "tmux socket name (tmux -L)")
	detail := fs.String("detail", "", "the triggering event's tool and command")
	agent := fs.String("agent", "", "the subagent that triggered the event, if any")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
		return 1
	}
	pane := fs.Arg(0)

	logf, closeLog := openJudgeLog()
	defer closeLog()

	// The judge derives its own configuration location: config.DefaultPath
	// under the operator's home, never a flag, never the environment, and
	// never a tmux option. A judge is spawned from inside the pane it is
	// about to judge, so $COOP_CONFIG there is that session's to set — and
	// this file's directory holds arbiter.md, the policy the judge is
	// about to obey, and is the parent claude discovers a CLAUDE.md from.
	// The socket is no better: every process in a monitored pane inherits
	// a $TMUX pointing at coop's own server, so a global option is one
	// approved tool call from being rewritten by the session it gates.
	cfgPath := config.DefaultPath()
	if cfgPath == "" {
		// No resolvable home: filepath.Dir would give ".", and seeding the
		// policy would write arbiter.md and arbiter/ into the judge's cwd
		// — the monitored repo, the exact directory ArbiterHome exists to
		// keep the judge out of. A judge with no policy to read does
		// nothing.
		logf("no resolvable config path: not judging %s", pane)
		return 1
	}
	cfgDir := filepath.Dir(cfgPath)
	policy, err := hub.LoadArbiterPolicy(cfgDir)
	if err != nil {
		logf("policy: %v", err)
		return 1
	}
	dir, err := hub.ArbiterHome(cfgDir)
	if err != nil {
		logf("arbiter home: %v", err)
		return 1
	}
	model, allowed := judgeConfig(cfgPath, logf)

	tm := &hub.ExecTmux{Socket: *socket}
	err = hub.Judge(tm, hub.DefaultTranscripts(), hub.JudgeReq{
		PaneID:  pane,
		Detail:  *detail,
		Agent:   *agent,
		Allowed: allowed,
		Audit:   hub.DefaultAuditPath(),
		Now:     time.Now,
		Run:     hub.ClaudeJudgeRunner(dir, model, hub.JudgeSystemPrompt(policy)),
		Log:     logf,
	})
	if err != nil {
		logf("judge %s: %v", pane, err)
		return 1
	}
	return 0
}

// judgeConfig resolves the model and the send gate from the config
// file. Three cases, not two — an unparseable file is not a missing one:
//
//   - no file: the built-in defaults, the same as an operator who never
//     wrote one.
//   - a file that parses: its settings, including a present-but-empty
//     allowed_cmds, which means send nothing.
//   - a file that does not parse: no send gate at all, logged. The
//     failure this closes is the operator who set allowed_cmds to []
//     and later introduced a syntax error — or wrote "claude" where the
//     list belongs, which fails the whole Config unmarshal. Falling back
//     to the built-in claude,node there silently *widens* a gate the
//     file was narrowing, which is the one direction a config error must
//     never move. Escalating with no send gate is the same behaviour a
//     deliberate [] asks for.
//
// The model is not treated the same way: a default model on a broken
// file costs a cheaper judgement, not a wrong keystroke.
func judgeConfig(cfgPath string, logf func(string, ...any)) (string, []string) {
	c, err := config.Load(cfgPath)
	switch {
	case err == nil:
		model := hub.DefaultArbiterModel
		if c.Arbiter.Model != "" {
			model = c.Arbiter.Model
		}
		return model, judgeAllowedCmds(c)
	case os.IsNotExist(err):
		return hub.DefaultArbiterModel, judgeAllowedCmds(config.Config{})
	default:
		logf("config: %v: judging with no send gate (escalate only)", err)
		return hub.DefaultArbiterModel, nil
	}
}

// judgeAllowedCmds is the pane_current_command allowlist a verdict's
// digit is gated by. A present-but-empty arbiter.allowed_cmds means
// "send nothing" — the most restrictive setting an operator can write,
// exactly as -allowed-cmds "" already means for the TUI's own digit
// keys; only an absent key falls back to the built-in list. splitCmds,
// not the raw entries, so "claude, node" written as one string or as two
// means the same list the hub's flag would parse.
func judgeAllowedCmds(c config.Config) []string {
	if c.Arbiter.AllowedCmds == nil {
		return splitCmds(hub.DefaultAllowedCmds)
	}
	return splitCmds(strings.Join(c.Arbiter.AllowedCmds, ","))
}

// openJudgeLog appends to the judge log, falling back to a no-op logger
// when it cannot be opened — a judge with nowhere to log still judges.
func openJudgeLog() (func(string, ...any), func()) {
	path := hub.DefaultJudgeLogPath()
	if path == "" {
		return func(string, ...any) {}, func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func(string, ...any) {}, func() {}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return func(string, ...any) {}, func() {}
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(f, "%s "+format+"\n",
			append([]any{time.Now().Format(time.RFC3339)}, args...)...)
	}
	return logf, func() { f.Close() }
}
