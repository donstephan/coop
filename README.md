# coop

**TL;DR** — Running several Claude Code sessions at once and losing track of
which one is waiting on you? `coop` is a tmux TUI that shows all of
them in one place: a status list (needs input / done / working / idle), a
live preview of the selected session, and one-key jumps in and out.

```bash
go build -o ~/.local/bin/coop ./cmd/coop
coop
```

## Why

Each Claude Code session sits in its own terminal, and the one that needs
your attention is never the one you're looking at. coop gives you a single
screen where:

- every session's state is visible at a glance — and a session that just
  finished shows `done` until you've looked at it, so fresh answers stand
  out from long-idle sessions
- the session list sits on the left; the right half is a **live view** of
  the selected session — real, scrollable, clickable
- `Tab` jumps straight to the next session that needs input, `Enter` puts
  your keyboard in the live view, `Shift+←/→` hops between list and view
- you can answer a numbered dialog (`❯ 1. Yes`) without leaving the
  dashboard — just press the digit

## Quick start

1. Build and run (needs Go and tmux ≥ 3.2):

   ```bash
   go build -o ~/.local/bin/coop ./cmd/coop
   coop
   ```

   Run it from any terminal — coop starts its own tmux server and lands you
   in the dashboard (the `roost` session). Your personal tmux.conf is left
   untouched; coop uses a dedicated socket with its own settings.

2. Press `n` to create a session: pick a repo, and a fresh `claude` starts
   in its own tmux session.

3. Watch the list. When a session shows `input`, hit `Tab` to select it and
   answer a dialog with a digit right from the list — or `Enter` to put
   your keyboard in the live view and type freely. `Shift+←` brings focus
   back to the list.

Tip: the `n` picker's last row is `+ add new repo` — pick it, type a path,
and the repo is written to `~/.config/coop/config.json` before the session
starts. You can also fill the list in by hand:

```json
{
  "repos": ["~/proj/foo", "~/proj/bar"]
}
```

## Keys

| Key | Action |
|-----|--------|
| `↑/↓` or `k/j` | select session |
| `Tab` | jump to the next session needing input (cycles) |
| `Enter` | focus the live view of the selected session |
| `Shift+←/→` | switch between the session list and the live view |
| `0-9` | pass the digit to the selected session (answers dialogs; otherwise lands in the input box) |
| `Space` | apply the digit the arbiter suggested for the selected session |
| `Backspace` | pass backspace to the selected session (erase a stray digit) |
| `/` | start a slash command in the selected session (types `/` there, then focuses the live view) |
| `n` | create a new session (repo picker; its last row adds a repo to the config) |
| `a` | cycle the arbiter: off → recommend → full → off (turning off confirms like `x`) |
| `x` | kill the selected session (`y` confirms, `esc` cancels) |
| `q` | quit (`y` quits, `k` kills all sessions & quits, `esc` cancels; `ctrl+c` quits immediately). `k` asks again if sessions are still working. |
| `?` | toggle the full key list in the footer |

Digit/backspace passthrough refuses unless the pane is running an allowed
command (`-allowed-cmds`, default `claude,node`) — so a dead claude's shell
never receives stray keystrokes.

## Arbiter

Some needs-input dialogs are routine under a standing policy ("yes, run
the tests") and don't need a human keystroke. **Step zero, before turning
this on:** tighten your per-repo Claude Code permission allowlists —
deterministic settings should eat the truly routine prompts, and the
arbiter is for the genuinely ambiguous residue that's left over, not a
substitute for `settings.json`.

Press `a` to cycle a real `claude --model sonnet` session — named
`arbiter`, visible in the list like any other session, selectable and
killable — through **off → recommend → full → off**:

- `recommend` starts the arbiter but it only annotates a dialog with a
  suggestion, never answers it.
- `full` lets it answer numbered dialogs (a single digit) under policy.
  Free-text prompts always get triage only — the arbiter never types
  prose into a session.
- Cycling back to off kills the arbiter session behind the same confirm
  `x` uses (`y` confirms, `esc` cancels).

Its judgment comes from `~/.config/coop/arbiter.md`, a freeform markdown
policy file seeded on first launch with a conservative template (escalate
unless clearly routine; never approve pushes, deletes, installs, or
anything irreversible). The arbiter is stateless between episodes, so
edits only take effect on restart — kill it (`a`, `y`) and press `a`
again.

When it escalates, the selected session's row gets a marker and a
one-line detail underneath it — `arbiter: <note>` — until the session
leaves needs-input; when it answers, that line instead reads
`answered <digit> by arbiter <age> ago`.

An escalation about a numbered dialog usually names the option the
arbiter would have picked. When it does, the footer offers
`space apply <digit>` and `Space` sends that digit — the recommend-mode
loop is read the note, press one key. It goes out through the same
allowed-command gate as the digit keys, and the suggestion is dropped as
soon as the session stops needing input, so `Space` can't replay a stale
answer.

Every action, answered or escalated, is appended to
`~/.local/state/coop/arbiter-audit.jsonl` — a durable record coop itself
never reads.

The arbiter's only hands are a helper CLI, also usable by you:
`coop peek <session>` (read the dialog and last transcript turn),
`coop answer <session> <digit> <reason...>` (send a digit and audit it),
`coop note [-suggest N] <session> <text...>` (leave an escalation note,
optionally naming the digit `Space` applies). `answer` is
the only write path, and it's gated server-side regardless of what the
model attempts: it refuses coop's own sessions and the arbiter itself, a
pane whose current command isn't in the allowlist, a screen not
currently showing a dialog, and any attempt in `recommend` mode.
`-suggest` is not one of those gates — nothing is sent until you press
the key — so it is an ordinary flag. The
helper CLI otherwise takes only `-socket`; the audit path and allowlist come from
the environment (`XDG_STATE_HOME`, `COOP_ALLOWED_CMDS` — the arbiter
session inherits the hub's `-allowed-cmds` value), never from flags the
model could pass itself.

## Config

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-socket` | `COOP_SOCKET` | `coop` | tmux socket name (`tmux -L`) |
| `-allowed-cmds` | `COOP_ALLOWED_CMDS` | `claude,node` | commands quick-send may target |
| `-done-ttl` | `COOP_DONE_TTL` | `5m` | how long a finished session shows `done` before decaying to idle (`0` disables) |
| `arbiter.model` (config.json) | — | `sonnet` | model the arbiter session runs (`claude --model <model>`) |

`~/.config/coop/config.json` holds the repo list for the `n` picker and,
optionally, tmux overrides (see below). The picker's `+ add new repo` row
appends to `repos`; anything else in the file is left as you wrote it:

```json
{
  "repos": ["~/proj/foo"],
  "tmux": ["set -g history-limit 100000", "set -g mouse off"],
  "arbiter": {"model": "sonnet"}
}
```

### tmux setup

You don't need any tmux configuration — coop applies its own at launch:
dashboard plumbing (`monitor-bell` latches needs-input, `status off` keeps
the live pane clean) plus terminal QoL
(true color, snappy ESC, mouse, titles, OSC 52 clipboard). No tmux.conf is
read on the coop socket, including your personal one.

To override or extend any of it, add entries to the `tmux` list in
`config.json` — each entry is one tmux command, chained after the defaults
(last one wins) whenever coop is launched from outside tmux.

## How it works

- Each `claude` runs in its own tmux session on a dedicated socket
  (`tmux -L coop`); coop lives in a `roost` session on that socket.
- There's no daemon, no hooks, no state file — coop polls
  `tmux list-panes` once a second and rebuilds its view from scratch.
  tmux *is* the database.
- Status comes from the pane title Claude Code already sets (spinner =
  working, 🔔/bell = needs input), plus a screen-capture fallback that
  spots an on-screen dialog when the title alone doesn't say so.
- The live preview is a real nested tmux client attached to the selected
  session, so scrolling and clicking just work.
- `done` is the one status with memory across polls: a session that
  finishes working shows `done` until you visit it (focus the preview on
  it, or attach directly) or `-done-ttl` elapses. It's stored as tmux user
  options on the panes themselves, so multiple coop instances share one
  view — but a full server restart forgets it.
- The list groups sessions by repo, alphabetically, and never reorders on
  status — rows stay where you left them.

## Tests

```bash
go test ./...            # unit tests
scripts/e2e-smoke.sh     # end-to-end on a throwaway tmux socket
```

## License

MIT — see [LICENSE](LICENSE).
