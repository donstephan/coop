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
| `a` | cycle the arbiter: off → recommend → full → off |
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

Press `a` to cycle the arbiter through **off → recommend → full → off**.
It isn't a session you can see: when a session stops for input, coop runs
one short-lived headless `claude` in the background for that dialog alone
and applies what it decides.

- `recommend` only annotates a dialog with a suggestion, never answers it.
- `full` lets it answer numbered dialogs (a single digit) under policy.
  Free-text prompts always get triage only — the arbiter never types
  prose into a session.
- Cycling back to off takes effect at once, with no confirm — there's no
  session to kill.

Its judgment comes from `~/.config/coop/arbiter.md`, a freeform markdown
policy file seeded on first use with a conservative template (escalate
unless clearly routine; never approve pushes, deletes, installs, or
anything irreversible). Every dialog gets a fresh process that re-reads
the file, so an edit takes effect on the next one — no restart.

Turning the arbiter on doesn't reach backwards: sessions already sitting
at a prompt when you press `a` stay untriaged until their next dialog.

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
never reads. Failures and anything the model returned that coop couldn't
use land in `~/.local/state/coop/judge.log` beside it.

The arbiter has no hands and no CLI to reach for: it runs with an empty
tool set (`--tools ""`), so "it only reads a screen and answers" is
enforced on the command line rather than left to a settings file. It
returns a small JSON verdict — answer this digit, or escalate with this
reason — and coop applies it behind gates it re-checks itself: coop's own
sessions are refused, so is a pane whose current command isn't in the
allowlist, a screen not currently showing a dialog, and any answer at all
in `recommend` mode. The verdict names no pane either — it is applied to
the exact pane that was judged, and only while that pane is still on the
*same* dialog the judge looked at. Answer a prompt yourself while a
judgement is in flight and its digit is dropped, not delivered to
whatever opened next.

Those gates are yours, so coop reads them only from your files: the
allowlist is `arbiter.allowed_cmds` in `config.json` (an empty list means
"send nothing"), the policy is `~/.config/coop/arbiter.md`, and the judge
runs with a constructed environment — a `PATH` built from where coop
itself is installed plus the system directories, a `HOME` from the
password database, and nothing else through except locale, timezone and
`TMUX_TMPDIR` (it has to find the same tmux server you're on). Its audit
log always lands under `~/.local/state/coop`, wherever `XDG_STATE_HOME`
points, so a redirected variable can't silence the record.
A judge is spawned from inside the session it is about to judge, so
anything that session can set — its environment, or an option on coop's
tmux socket — is treated as untrusted and replaced.

**This is defence in depth, not a sandbox.** coop and the sessions it
watches run as the same user on the same tmux server, so a session that
has already been talked into running arbitrary commands can turn the
arbiter on, write to your config, or replace the coop binary; nothing
in-band can stop that. What the gates do buy is that an unattended judge
misfiring — a stale screen, a dead session's shell, a verdict about a
dialog you already answered — doesn't turn into a keystroke, and that
every answer it does send is on the record. Keep your per-repo Claude
Code permissions tight; the arbiter is not a substitute for them.

If your Anthropic auth lives in environment variables, put it in
`~/.claude/settings.json`'s `env` block instead — the judge does not
inherit `ANTHROPIC_*` from anywhere.

`coop peek <session>` survives as a debug aid for you: it prints a
session's screen and its last assistant turn, the same context the
arbiter is handed.

## Config

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-socket` | `COOP_SOCKET` | `coop` | tmux socket name (`tmux -L`) |
| `-allowed-cmds` | `COOP_ALLOWED_CMDS` | `claude,node` | commands quick-send may target |
| `-done-ttl` | `COOP_DONE_TTL` | `5m` | how long a finished session shows `done` before decaying to idle (`0` disables) |
| `arbiter.model` (config.json) | — | `haiku` | model each triage episode runs (`claude -p --model <model>`) |
| `arbiter.allowed_cmds` (config.json) | — | `["claude", "node"]` | commands a judge's answer may target (`[]` = never send) |

`~/.config/coop/config.json` holds the repo list for the `n` picker and,
optionally, tmux overrides (see below). The picker's `+ add new repo` row
appends to `repos`; anything else in the file is left as you wrote it:

```json
{
  "repos": ["~/proj/foo"],
  "tmux": ["set -g history-limit 100000", "set -g mouse off"],
  "arbiter": {"model": "haiku", "allowed_cmds": ["claude", "node"]}
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
