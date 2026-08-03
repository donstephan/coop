package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"coop/internal/hub"
)

// isArbiterCmd reports whether argv selects the peek subcommand — a
// human debug aid that prints a session's screen and last assistant
// message. answer and note used to live here too, as the arbiter
// session's tool surface; the judge produces a verdict coop applies
// through hub.Answer/hub.Note directly, so there is no longer a CLI
// surface a model could reach for at all.
func isArbiterCmd(args []string) bool {
	return len(args) > 0 && args[0] == "peek"
}

// cliSocket is the helper CLI's default socket: COOP_SOCKET beats the
// socket this shell's tmux session lives on ($TMUX's socket path — a
// peek run from a pane on the coop socket lands on the right server
// without passing -socket), which beats the "coop" default.
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
// Refusals go to stderr with the reason.
func runArbiterCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coop "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", cliSocket(), "tmux socket name (tmux -L)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	tm := &hub.ExecTmux{Socket: *socket}
	pos := fs.Args()
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: coop peek <session>")
		return 2
	}
	out, err := hub.Peek(tm, hub.DefaultTranscripts(), pos[0])
	if err != nil {
		fmt.Fprintln(stderr, "coop:", err)
		return 1
	}
	fmt.Fprint(stdout, out)
	return 0
}
