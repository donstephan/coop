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

Tip: add your repos to `~/.config/coop/config.json` so the `n` picker knows
about them:

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
| `Backspace` | pass backspace to the selected session (erase a stray digit) |
| `/` | start a slash command in the selected session (types `/` there, then focuses the live view) |
| `n` | create a new session (repo picker) |
| `x` | kill the selected session (`y` confirms, `esc` cancels) |
| `q` | quit (`y` quits, `k` kills all sessions & quits, `esc` cancels; `ctrl+c` quits immediately). `k` asks again if sessions are still working. |
| `?` | toggle the full key list in the footer |

Digit/backspace passthrough refuses unless the pane is running an allowed
command (`-allowed-cmds`, default `claude,node`) — so a dead claude's shell
never receives stray keystrokes.

## Config

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-socket` | `COOP_SOCKET` | `coop` | tmux socket name (`tmux -L`) |
| `-allowed-cmds` | `COOP_ALLOWED_CMDS` | `claude,node` | commands quick-send may target |
| `-done-ttl` | `COOP_DONE_TTL` | `5m` | how long a finished session shows `done` before decaying to idle (`0` disables) |

`~/.config/coop/config.json` holds the repo list for the `n` picker and,
optionally, tmux overrides (see below):

```json
{
  "repos": ["~/proj/foo"],
  "tmux": ["set -g history-limit 100000", "set -g mouse off"]
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
