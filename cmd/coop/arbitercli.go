package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"coop/internal/hub"
)

// isArbiterCmd reports whether argv selects a helper subcommand — the
// arbiter's tool surface, dispatched before the TUI's flag parsing.
func isArbiterCmd(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "peek", "answer", "note":
		return true
	}
	return false
}

// cliSocket is the helper CLI's default socket: COOP_SOCKET beats the
// socket this shell's tmux session lives on ($TMUX's socket path — the
// arbiter runs inside the coop socket, so its calls land on the right
// server without passing -socket), which beats the "coop" default.
func cliSocket() string {
	if v := os.Getenv("COOP_SOCKET"); v != "" {
		return v
	}
	if t := os.Getenv("TMUX"); t != "" {
		if path, _, ok := strings.Cut(t, ","); ok && path != "" {
			return filepath.Base(path)
		}
	}
	return "coop"
}

// runArbiterCLI runs one helper subcommand and returns the exit code.
// Refusals go to stderr with the reason — the arbiter reads them to
// learn why an action was rejected and escalates instead of retrying.
func runArbiterCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coop "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	// -audit and -allowed-cmds are deliberately not flags here: the
	// arbiter's only write access is the Bash(coop answer:*)-style
	// prefix allowlist, so any gate this CLI takes as an argument is
	// one the model could try to widen. The audit path and allowlist
	// instead come from the operator's env (still honoring
	// XDG_STATE_HOME / COOP_ALLOWED_CMDS), which the arbiter can't set
	// — an env-prefixed command wouldn't match those prefix patterns.
	socket := fs.String("socket", cliSocket(), "tmux socket name (tmux -L)")
	// -suggest is not a gate — it names the digit the human's space key
	// applies, so it is a flag on note only; the other verbs reject it
	// rather than accepting an argument that does nothing.
	var suggest *string
	if args[0] == "note" {
		suggest = fs.String("suggest", "", "digit the human can apply with space")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	tm := &hub.ExecTmux{Socket: *socket}
	audit := hub.DefaultAuditPath()
	cmds := envOr("COOP_ALLOWED_CMDS", "claude,node")
	pos := fs.Args()
	var warn string
	var err error
	switch args[0] {
	case "peek":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "usage: coop peek <session>")
			return 2
		}
		var out string
		if out, err = hub.Peek(tm, hub.DefaultTranscripts(),
			hub.DefaultClaudeSessions(), pos[0]); err == nil {
			fmt.Fprint(stdout, out)
		}
	case "answer":
		if len(pos) < 3 {
			fmt.Fprintln(stderr, "usage: coop answer <session> <digit> <reason...>")
			return 2
		}
		warn, err = hub.Answer(tm, hub.AnswerReq{Session: pos[0], Digit: pos[1],
			Reason: strings.Join(pos[2:], " "), Allowed: splitCmds(cmds),
			Audit: audit, Now: time.Now()})
	case "note":
		if len(pos) < 2 {
			fmt.Fprintln(stderr, "usage: coop note [-suggest N] <session> <text...>")
			return 2
		}
		warn, err = hub.Note(tm, hub.NoteReq{Session: pos[0],
			Text: strings.Join(pos[1:], " "), Suggest: *suggest,
			Audit: audit, Now: time.Now()})
	}
	if warn != "" {
		fmt.Fprintln(stderr, "coop: warning:", warn)
	}
	if err != nil {
		fmt.Fprintln(stderr, "coop:", err)
		return 1
	}
	return 0
}
