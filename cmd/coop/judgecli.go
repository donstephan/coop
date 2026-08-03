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
func judgeArgv(self, socket, pane, detail string) []string {
	return []string{self, "judge", "-socket", socket, "-detail", detail, pane}
}

// runJudgeCLI runs one triage episode and returns an exit code nobody
// reads — the process is detached, so its only outputs are the tmux
// options Judge writes and the log. Failures are logged, never surfaced.
func runJudgeCLI(args []string) int {
	fs := flag.NewFlagSet("coop judge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "coop", "tmux socket name (tmux -L)")
	detail := fs.String("detail", "", "the triggering event's tool and command")
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
	model, allowed := hub.DefaultArbiterModel, judgeAllowedCmds(config.Config{})
	if c, err := config.Load(cfgPath); err == nil {
		if c.Arbiter.Model != "" {
			model = c.Arbiter.Model
		}
		allowed = judgeAllowedCmds(c)
	}

	tm := &hub.ExecTmux{Socket: *socket}
	err = hub.Judge(tm, hub.DefaultTranscripts(), hub.JudgeReq{
		PaneID:  pane,
		Detail:  *detail,
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
