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

There is no daemon, no hooks, no state file. The model is a `[]hub.Pane` rebuilt from scratch by a 1-second `tmux list-panes` poll; `Status` is derived fresh every poll (`DeriveStatuses`), never stored. Status comes from three sources, in order:

1. **Claude Code's own published state** (`internal/hub/claudestate.go`) — it writes `~/.claude/sessions/<pid>.json` per process, carrying `status` (`busy`/`waiting`/`idle`), `sessionId`, a derived `name`, and `statusUpdatedAt`. `#{pane_pid}` joins a pane to its file. This is undocumented internal state (shape observed on 2.1.219), so every read is best-effort and an unrecognized `status` string falls through to the heuristics below rather than mislabelling a pane. Files outlive a SIGKILLed process, so the file's `procStart` is checked against `/proc/<pid>/stat` field 22 before it is trusted.
2. **The pane title** Claude Code sets (braille spinner = working, 🔔/bell = needs input).
3. **A screen-capture fallback** (`NeedsInputScreen`) detecting an on-screen dialog by its `❯ 1.` option row — skipped for panes that published their own status, which is most of the poll's tmux traffic.

The time column ages from `Pane.Since()` — Claude's `statusUpdatedAt` where there is one, else the session start — so a row reads "waiting 6m", not "session started 3h ago".

### The stat column (`internal/hub/transcript.go`)

`s` cycles an optional right-hand column: off → context → model. Off is the default, and the poll skips transcript I/O entirely while it is (`nil` stats func), so the nav keeps its pinned `navWidth`. Turning it on widens the pane to `navWidth+statWidth` via the same `resizeSelf` path a terminal resize uses.

`Transcripts` reads `~/.claude/projects/<slug>/<sessionId>.jsonl`, resolving the file from the `sessionId` in the pane's published state (with a by-id glob fallback, since a session outlives a directory rename). Only the last `tailBytes` are read and only the last main-thread assistant turn is parsed — subagent turns (`isSidechain`) carry unrelated context. Results cache on mtime+size so a 1-second poll doesn't re-parse. Context is that turn's `input + cache_read + cache_creation`, so it lags by a turn; there is no published window limit (sessions run both 200k and 1M), so it renders as an absolute count, never a percentage. Anything missing renders blank, never `0k`.

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

**Never put paths from the developer's own machine into tests or fixtures** — no `/home/<username>/…`, no real home directory, no absolute path to this checkout. Fixture paths are always generic (`/home/user/coop`, `/home/user/Documents/coop/git/coop`, `/home/user/sprocket-v2`); anything the test actually writes to goes under `t.TempDir()`. The same goes for repo and session names — invent them (`sprocket-v2`, `alpha`, `beta`), never borrow a real project's. Tests must pass for any user on any machine, and a real path leaks the developer's environment into the repo.
