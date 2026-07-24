# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`coop` is a tmux-native TUI for monitoring many Claude Code sessions at once: each `claude` runs in its own tmux session on a dedicated socket (`tmux -L coop`), and the TUI (living in a `roost` session on that socket) shows every session's status, previews the selected one live, and forwards keys to it.
## Commands

```bash
go build -o ~/.local/bin/coop ./cmd/coop   # build/install
go test ./...                              # unit tests
go test ./internal/tui -run TestName       # single test
scripts/e2e-smoke.sh                       # e2e on a throwaway tmux socket (needs tmux ≥ 3.2)
```

## Architecture

Three packages under a thin `cmd/coop/main.go`:

- **`internal/hub`** — everything tmux. The `Tmux` interface is the subset of the tmux CLI the app needs; `ExecTmux` shells out (`tmux -L <socket> …`), and unit tests substitute the fake in `internal/hub/fake_tmux_test.go`. Also holds status derivation, done-tracking, sort order, session naming, live-pane command strings, and the shared focus palette.
- **`internal/tui`** — the Bubble Tea model. One file, `model.go`.
- **`internal/config`** — `~/.config/coop/config.json`: `repos` for the new-session picker, `tmux` override commands.

### Core design: tmux is the database

There is no daemon, no hooks, no state file. The model is a `[]hub.Pane` rebuilt from scratch by a 1-second `tmux list-panes` poll; `Status` is derived fresh every poll (`DeriveStatuses`), never stored. Status comes from the pane title Claude Code sets (braille spinner = working, 🔔/bell = needs input), with a screen-capture fallback (`NeedsInputScreen`) that detects an on-screen dialog by its `❯ 1.` option row.

The one status needing memory across polls — `done` (finished working, not yet seen) — is persisted as tmux **user options on the tracked panes themselves** (`@coop_working`, `@coop_done_since`, managed by `DoneTracker`), so multiple hub instances on the socket share one view and state dies with its pane. Other user options: `@coop` marks a hub instance's own session (hidden from the list, used to reuse detached hubs), `@coop_live` marks the live preview pane (so a restarted TUI adopts it instead of stacking splits).

### Launch flow (`cmd/coop/main.go`)

Run outside tmux, coop bootstraps the server itself: one tmux invocation chains `start-server`, the built-in defaults (`tmuxDefaults`), config.json `tmux` overrides, and `new-session` running coop, then execs into `attach-session`. `-f /dev/null` keeps the user's tmux.conf off the socket. **Every chained tmux command must be idempotent** — the chain re-runs each time a hub launches against a running server (so plain `set`, never `set -a`). Run inside tmux, it goes straight to the TUI. Hub sessions are named `roost`, `roost-2`, … so several hubs (one per monitor) share a socket; a detached hub is reattached rather than duplicated.

### TUI conventions (`internal/tui/model.go`)

- All tmux I/O happens inside `tea.Cmd` closures that capture what they need **by value** — the Model is never touched from inside a Cmd.
- `Update` returns at most one command per message ("single-command invariant") so tests can drive it message-by-message; chains are expressed as message → command → message (e.g. `livePaneMsg` → `resizeSelf` → `resizedMsg` → `retarget`).
- Nearly all tmux writes are best-effort with a comment saying so — a failure leaves state stale and the next poll tick retries/self-heals. Follow that pattern.
- The live preview is a real nested tmux client (`TMUX= tmux attach -f ignore-size`) in a split pane, retargeted by `respawn-pane` when the selection changes; `ignore-size` keeps it out of window-size negotiation (`HasPrimaryClient` distinguishes it from real clients).

### tmux targeting gotchas (see comments in `internal/hub/tmux.go`)

Exact-match `=` prefixes and trailing `:` on session targets are load-bearing (`"=name"` vs `"=name:"` vs bare `name:` differ per command); don't simplify them without reading the adjacent comment.

## Testing

Unit tests never touch a real tmux: `hub` tests parse canned output or use `fakeTmux`; `tui` tests construct a `Model` and feed `Update` messages directly, asserting on returned commands' messages. `scripts/e2e-smoke.sh` covers the real-tmux integration (discovery, status, live preview, retarget, create, kill, quit) — run it when changing launch, live-pane, or tmux plumbing.
