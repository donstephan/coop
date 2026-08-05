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
| `a` | toggle the arbiter (off ↔ recommend) |
| `s` | cycle the right-hand stat column: off → context → model |
| `pgup`/`pgdn` | scroll the footer message box when a message overflows it |
| `x` | kill the selected session (`y` confirms, `esc` cancels) |
| `q` | quit (`y` quits, `k` kills all sessions & quits, `esc` cancels; `ctrl+c` quits immediately). `k` asks again if sessions are still working. |
| `?` | toggle the full key list in the footer |

Every key coop sends on your behalf — the digits, `Space`, backspace, and
the `/` that starts a slash command — refuses unless the pane is running
an allowed command (`-allowed-cmds`, default `claude,node`), so a dead
claude's shell never receives stray keystrokes.

## Arbiter

Some needs-input dialogs are routine under a standing policy ("yes, run
the tests") and the cost of them is not the keystroke, it's walking over
to find out which ones they were. **Step zero, before turning this on:**
tighten your per-repo Claude Code permission allowlists —
deterministic settings should eat the truly routine prompts, and the
arbiter is for the genuinely ambiguous residue that's left over, not a
substitute for `settings.json`.

Press `a` to toggle the arbiter **off ↔ recommend**. It isn't a session
you can see: when a session stops for input, coop runs one short-lived
headless `claude` in the background for that dialog alone, and turns what
it decides into a line of text on that session's row.

That is the whole of it — the arbiter annotates and never sends. It has
no path to a session's keyboard at all, so the keystroke that answers a
dialog is always yours; what the arbiter buys you is knowing which
dialog is worth walking over to. Toggling it back off takes effect at
once, with no confirm — there's no session to kill.

Its judgment comes from `~/.config/coop/arbiter.md`, a freeform markdown
policy file seeded on first use with a conservative template (suggest
nothing unless clearly routine; never bless pushes, deletes, installs, or
anything irreversible). Every dialog gets a fresh process that re-reads
the file, so an edit takes effect on the next one — no restart. The model
is `arbiter.model` in `config.json`, default `sonnet`: one bounded prompt
and a one-line verdict per dialog, and since that line is the entire
product it's worth a good model — set the key if you disagree.

Turning the arbiter on doesn't reach backwards: sessions already sitting
at a prompt when you press `a` stay untriaged until their next dialog.

A judged session's row gets a marker, and the selected row's note is
spelled out underneath the list — `arbiter: <note>` — until that session
leaves needs-input.

A note about a numbered dialog usually names the option the arbiter would
have picked. When it does, the footer offers `space apply <digit>` and
`Space` sends that digit — the loop is read the note, press one key. The
send is yours: it goes out through the same allowed-command gate as the
digit keys, and the suggestion is dropped as soon as the session stops
needing input, so `Space` can't replay a stale answer.

Every note is appended to
`~/.local/state/coop/arbiter-audit.jsonl` — a durable record coop itself
never reads. `~/.local/state/coop/judge.log` beside it is the diagnostic
one: a line per episode saying which pane it was, whether the screen it
captured actually showed a dialog, which subagent asked (if any) and what
the verdict was, plus failures and anything the model returned that coop
couldn't use. It records the shape of an episode, never the screen text,
and it is never rotated.

The arbiter has no hands and no CLI to reach for: it runs with an empty
tool set (`--tools ""`) and `--strict-mcp-config`, so "it only reads a
screen and writes a line" is enforced on the command line rather than
left to a settings file, and there is no coop verb left for a model to
name even if it could run one. What comes back is a small JSON verdict —
a one-line reason and at most a single digit — and every field of it is
re-validated before any of it lands, because a garbled verdict should be
a row that says nothing rather than a row that says something wrong. The
verdict names no pane, either: the note goes to the exact pane that was
judged, so a split session's other pane never wears it.

The rest is about keeping the judge out of the reach of the session it is
judging — it is spawned from inside that very session, so anything that
session can set is treated as untrusted and replaced. Its policy is
`~/.config/coop/arbiter.md` and its working directory is an empty one coop
owns, so the repo under judgement can't hand it a `CLAUDE.md`. It runs
with a constructed environment — a `PATH` built from where coop itself is
installed plus the system directories, a `HOME` from the password
database, and nothing else through except locale, timezone and
`TMUX_TMPDIR` (it has to find the same tmux server you're on). Its audit
log always lands under `~/.local/state/coop`, wherever `XDG_STATE_HOME`
points, so a redirected variable can't silence the record.

**This is defence in depth, not a sandbox.** coop and the sessions it
watches run as the same user on the same tmux server, so a session that
has already been talked into running arbitrary commands can turn the
arbiter on, write to your config, or replace the coop binary; nothing
in-band can stop that. What this does buy is that an unattended judge
misfiring — a stale screen, a poisoned transcript, a verdict about a
dialog you already answered — costs you one wrong line of text next to a
row you were going to look at anyway, and leaves it on the record. Keep
your per-repo Claude Code permissions tight; the arbiter is not a
substitute for them.

If your Anthropic auth lives in environment variables, put it in
`~/.claude/settings.json`'s `env` block instead — the judge does not
inherit `ANTHROPIC_*` from anywhere.

When the dialog belongs to a subagent rather than the session's main
thread, the judge is told so by name. It still sees the main thread's
last message — that's the plan the subagent was dispatched under — but
labelled as the main thread's, so a `Bash` prompt from an `Explore`
agent isn't judged against whatever the parent happened to be narrating.

`coop peek <session>` survives as a debug aid for you: it prints a
session's screen and its last assistant turn, most of the context the
arbiter is handed.

## Toolbox

A repo can declare the command-line tooling its sessions need — a python
with the right libraries, `mongosh`, a pinned `terraform` — in a
`.coop/tools.Dockerfile` beside its code, and coop puts that tooling on
the session's `PATH` through transparent shims backed by one container
per repo. The repo is mounted at its own host path, so a traceback, a
config file's absolute path and anything claude then opens with `Read`
all name the same file; the container starts on the first command that
uses it and exits itself when it has been idle. With no config at all a
session gets `python3`, `pip3`, `jq`, `gh`, `curl` and `make` from the
base image; with no docker installed nothing changes at all. It is a
reproducible toolchain and a clean host, **not** a sandbox — the repo is
mounted read-write and the container runs as you. See
[docs/toolbox.md](docs/toolbox.md).

## Config

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-socket` | `COOP_SOCKET` | `coop` | tmux socket name (`tmux -L`) |
| `-config` | `COOP_CONFIG` | `~/.config/coop/config.json` | the file below: repo list, tmux overrides, `arbiter.model` |
| `-claude-cmd` | `COOP_CLAUDE_CMD` | `claude` | the command a new session runs |
| `-hooks` | `COOP_HOOKS` | on | publish status through `coop hook` (see [How it works](#how-it-works)). `0` or `false` opts out, which drops those sessions to title-only status |
| `-allowed-cmds` | `COOP_ALLOWED_CMDS` | `claude,node` | commands quick-send may target (`""` = never send) |
| `-done-ttl` | `COOP_DONE_TTL` | `5m` | how long a finished session shows `done` before decaying to idle (`0` disables) |
| `arbiter.model` (config.json) | — | `sonnet` | model each triage episode runs (`claude -p --model <model>`) |

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
- There's no daemon and no state file — coop polls `tmux list-panes` once
  a second and rebuilds its view from scratch. tmux *is* the database, and
  Claude Code's own state lands there too, as user options on the panes.
- Status comes from Claude Code's hooks. Every session coop launches runs
  with `--settings`, which registers `coop hook` on the events coop cares
  about; the hook writes `busy`/`waiting`/`idle` onto its own pane, so a
  status costs no tmux traffic beyond the `list-panes` already running.
- The pane title Claude Code sets (spinner = working, 🔔/bell = needs
  input) is the fallback, and the whole story for panes that publish
  nothing: sessions you started outside coop, and anything launched with
  `-hooks=false`. Those rows still read working and needs-input, but an
  attached session never latches the bell, so one sitting on an open
  dialog reads idle rather than waiting.
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
