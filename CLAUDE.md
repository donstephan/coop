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
- **`internal/config`** — `~/.config/coop/config.json`: `repos` for the new-session picker, `tmux` override commands, `arbiter.model` (defaults to sonnet). `AddRepo` is the one writer: it rewrites the file from its own raw JSON, key by key in source order, so settings coop does not model survive and existing `~/` entries stay unexpanded. It refuses to rewrite a file that does not parse.

### Core design: tmux is the database

There is no daemon, no hooks, no state file. The model is a `[]hub.Pane` rebuilt from scratch by a 1-second `tmux list-panes` poll; `Status` is derived fresh every poll (`DeriveStatuses`), never stored. Status comes from three sources, in order:

1. **Claude Code's own published state** (`internal/hub/claudestate.go`) — it writes `~/.claude/sessions/<pid>.json` per process, carrying `status` (`busy`/`waiting`/`idle`), `sessionId`, a derived `name`, and `statusUpdatedAt`. `#{pane_pid}` joins a pane to its file. This is undocumented internal state (shape observed on 2.1.219), so every read is best-effort and an unrecognized `status` string falls through to the heuristics below rather than mislabelling a pane. Files outlive a SIGKILLed process, so the file's `procStart` is checked against `/proc/<pid>/stat` field 22 before it is trusted.
2. **The pane title** Claude Code sets (braille spinner = working, 🔔/bell = needs input).
3. **A screen-capture fallback** (`NeedsInputScreen`) detecting an on-screen dialog by its `❯ 1.` option row — skipped for panes that published their own status, which is most of the poll's tmux traffic.

The time column ages from `Pane.Since()` — Claude's `statusUpdatedAt` where there is one, else the session start — so a row reads "waiting 6m", not "session started 3h ago".

### The stat column (`internal/hub/transcript.go`)

`s` cycles an optional right-hand column: off → context → model. Off is the default, and the poll skips transcript I/O entirely while it is (`nil` stats func), so the nav keeps its pinned `navWidth`. Turning it on widens the pane to `navWidth+statWidth` via the same `resizeSelf` path a terminal resize uses.

`Transcripts` reads `~/.claude/projects/<slug>/<sessionId>.jsonl`, resolving the file from the `sessionId` in the pane's published state (with a by-id glob fallback, since a session outlives a directory rename). Only the last `tailBytes` are read and only the last main-thread assistant turn is parsed — subagent turns (`isSidechain`) carry unrelated context. Results cache on mtime+size so a 1-second poll doesn't re-parse. Context is that turn's `input + cache_read + cache_creation`, so it lags by a turn; there is no published window limit (sessions run both 200k and 1M), so it renders as an absolute count, never a percentage. Anything missing renders blank, never `0k`.

The one status needing memory across polls — `done` (finished working, not yet seen) — is persisted as tmux **user options on the tracked panes themselves** (`@coop_working`, `@coop_done_since`, managed by `DoneTracker`), so multiple hub instances on the socket share one view and state dies with its pane. Other user options: `@coop` marks a hub instance's own session (hidden from the list, used to reuse detached hubs), `@coop_live` marks the live preview pane (so a restarted TUI adopts it instead of stacking splits), and the `@coop_arbiter*` family below.

### The arbiter (`internal/hub/arbiter.go`, `cmd/coop/arbitercli.go`)

`a` cycles a triage assistant: off → recommend → full → off (killing it takes the same y/esc confirm as `x`). It is a real `claude` session named `arbiter` on the same socket, launched by `LaunchArbiter` with a seeded policy file and a `settings.json` that pre-allows its own tools, so it never permission-prompts itself.

State rides on tmux options, like everything else: `@coop_arbiter` marks the session, `@coop_arbiter_mode` holds `recommend`|`full`, `@coop_arbiter_seen` gates the first nudge, `@coop_arbiter_nudged` dedupes one needs-input episode, `@coop_arbiter_note` is the escalation note on a row, `@coop_arbiter_suggest` is the digit that note offered, `@coop_arbiter_last` is `digit|unix|reason` of the last answer. `ArbiterModeOf` reads anything but an explicit `full` as recommend — the safe mode is the default, never the accident.

In the nav the arbiter is a pane like any other — it stays in `m.panes` so selection, `paneAt` and retargeting need no special case — but it is drawn as infrastructure, not work: `SortPanes` pins it last whatever its workdir is named, `viewNav` opens it with a `─ arbiter ───` divider instead of a repo header and labels it `arbiter · <mode>` (Claude's derived title names whatever it last triaged, which reads as a session in that repo), and the header count leaves it out. `tab` skips it; `x` on it arms the same confirm `a`'s full → off does, since the session is literally named `arbiter`.

`ArbiterNudger.Apply` runs after `DeriveStatuses` and types a `NudgeText` line into the arbiter's pane when a session enters needs-input. Two gates matter: keys typed at a claude younger than ~0.5s are swallowed, so a freshly launched arbiter waits one poll tick (`@coop_arbiter_seen`); and with several hubs on the socket only the lexicographically first one nudges (`leads`), since the nudged marker is not a test-and-set.

An escalation can name the option it would pick — `coop note -suggest N` writes the digit to `@coop_arbiter_suggest`, kept apart from the note text so `space` applies a field rather than a number parsed out of prose. `space` then sends it down the same gated path as the `0-9` keys, in either mode (a full-mode arbiter that escalated still leaves a digit worth one key), and a `space apply N` chip joins the hints only while the selected row has one. A note without the flag clears any digit an earlier one left: a stale suggestion under fresh text is the one way that key sends a wrong answer. `ArbiterSuggestOf` re-checks the single-digit shape on read, since the TUI hands the value straight to send-keys.

The arbiter never touches raw tmux — it acts through `coop peek|answer|note`, dispatched in `main.go` before the TUI ever starts. `Answer` is the only write path and re-checks every gate at call time: full mode, not a coop-owned session, `pane_current_command` in the allowlist, and a dialog actually on screen. **The audit path and allowlist are deliberately not flags** — they come from the operator's env, because any gate the CLI accepted as an argument is one the model could try to widen. Actions append to `arbiter-audit.jsonl` under `$XDG_STATE_HOME`; coop never reads it back.

### Launch flow (`cmd/coop/main.go`)

Run outside tmux, coop bootstraps the server itself: one tmux invocation chains `start-server`, the built-in defaults (`tmuxDefaults`), config.json `tmux` overrides, and `new-session` running coop, then execs into `attach-session`. `-f /dev/null` keeps the user's tmux.conf off the socket. **Every chained tmux command must be idempotent** — the chain re-runs each time a hub launches against a running server (so plain `set`, never `set -a`). Run inside tmux, it goes straight to the TUI. Hub sessions are named `roost`, `roost-2`, … so several hubs (one per monitor) share a socket; a detached hub is reattached rather than duplicated.

### TUI conventions (`internal/tui/model.go`)

- All tmux I/O happens inside `tea.Cmd` closures that capture what they need **by value** — the Model is never touched from inside a Cmd.
- `Update` returns at most one command per message ("single-command invariant") so tests can drive it message-by-message; chains are expressed as message → command → message (e.g. `livePaneMsg` → `resizeSelf` → `resizedMsg` → `retarget`).
- Nearly all tmux writes are best-effort with a comment saying so — a failure leaves state stale and the next poll tick retries/self-heals. Follow that pattern.
- The footer is a message box above the key hints: `viewFooter` renders the first non-empty of `actionErr`, `errMsg`, `arbiterDetail()`, word-wrapped by `wrapMsg` and capped at `min(msgBoxLines, height/4)` visible lines, with `pgup`/`pgdn` scrolling and a `pgup/pgdn scroll` chip that appears only while it overflows. Rows are scarce at the pinned `navWidth`, so the box takes none when there is no message. `Update` is a thin wrapper over `update` that calls `syncMsg` — the one place the scroll rewinds when the text changes — so the single-command invariant still holds. Confirm prompts (`kill X?`, `quit coop?`) bypass the box and own the whole footer; they are questions, not messages.
- The `n` picker ends in a `+ add new repo` row: `repoIdx` one past the end of `filteredRepos()` selects it, `enter` opens a path prompt (`adding`/`repoPath`), and a good path goes through the injected `addRepo` writer → `repoAddedMsg` → `createCmd`, so the session starts in the repo it just saved. An empty repo list still opens the picker — that row is the only way to configure the first one. `pickerLayout` is the single source of the body's geometry: `viewPicker` draws from it and `pickerRowAt` maps clicks back through it, the way `paneAt` mirrors `viewNav`. It pins the add row to the bottom of the frame and scrolls the list under it, so a long config can't push the row out of reach.
- The live preview is a real nested tmux client (`TMUX= tmux attach -f ignore-size`) in a split pane, retargeted by `respawn-pane` when the selection changes; `ignore-size` keeps it out of window-size negotiation (`HasPrimaryClient` distinguishes it from real clients).

### tmux targeting gotchas (see comments in `internal/hub/tmux.go`)

Exact-match `=` prefixes and trailing `:` on session targets are load-bearing (`"=name"` vs `"=name:"` vs bare `name:` differ per command); don't simplify them without reading the adjacent comment.

Versions below 3.5 differ in two ways coop has to absorb, since Ubuntu 24.04 LTS ships 3.4. **`-F` output is run through `vis(3)`**, so the `\x1f` field separator arrives as the four characters `\037` — every parser splits with `splitFields`, never `strings.Split` on the raw byte. Getting this wrong is not a cosmetic failure: `parseMarkedPane` misses the live pane, so the TUI splits a fresh one every poll tick. And **`allow-set-title` does not exist**, so `livePaneOptions` sets it best-effort. More generally, a live pane that exists is adopted even when the command that made it returned an error — dropping the id is what turns any such failure into one new pane per second.

## Testing

Unit tests never touch a real tmux: `hub` tests parse canned output or use `fakeTmux`; `tui` tests construct a `Model` and feed `Update` messages directly, asserting on returned commands' messages. `scripts/e2e-smoke.sh` covers the real-tmux integration (discovery, status, live preview, retarget, create, kill, quit) — run it when changing launch, live-pane, or tmux plumbing.

**Never put paths from the developer's own machine into tests or fixtures** — no `/home/<username>/…`, no real home directory, no absolute path to this checkout. Fixture paths are always generic (`/home/user/coop`, `/home/user/Documents/coop/git/coop`, `/home/user/sprocket-v2`); anything the test actually writes to goes under `t.TempDir()`. The same goes for repo and session names — invent them (`sprocket-v2`, `alpha`, `beta`), never borrow a real project's. Tests must pass for any user on any machine, and a real path leaks the developer's environment into the repo.
