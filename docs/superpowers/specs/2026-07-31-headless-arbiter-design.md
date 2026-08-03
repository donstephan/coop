# Headless arbiter — design

Date: 2026-07-31

Replace the arbiter's long-lived `claude` tmux session with a one-off headless
`claude -p` process per needs-input episode, producing a structured verdict that
coop applies itself.

## Why

The current arbiter is an interactive `claude` session that coop types nudges
into with `send-keys`. Everything expensive about the design follows from that
one choice:

- **A readiness race.** A freshly launched claude swallows keys for ~0.5s, so
  nudges are gated behind `arbiterReadyAge` plus an extra second to absorb
  `#{session_created}`'s whole-second truncation. Panes that went waiting before
  the arbiter existed need `CatchupNudge` to cover them, which needs
  `@coop_arbiter_nudged` re-arming on failure so a later relaunch can retry.
- **Shared context across sessions.** Session A's screen text — untrusted data
  from a monitored session — stays in context while the arbiter judges session
  B. For a component whose job is approving tool calls, per-decision isolation
  is a real property, not hygiene.
- **Compaction.** A long-lived arbiter eventually compacts. A policy-following
  agent whose system prompt survives but whose recent reasoning was summarized
  is unpredictable in exactly the wrong way.
- **Serialized episodes.** Two panes going waiting at once interleave two lines
  into one composer; the arbiter batches them or drops one.

A one-off process has none of these. Its prompt arrives on stdin, so there is no
key-swallow window; its context holds one episode; it never compacts; and
concurrent episodes are concurrent processes.

The cost per nudge goes down, not up: preamble + policy + one screen, cached
across invocations, instead of dragging a growing context through every turn.

## Shape

**The arbiter stops being a thing that exists and becomes a thing that happens.**
No session, no pane, no nav presence.

### State

`a` still cycles off → recommend → full. Mode moves from a session option on the
arbiter session to a socket-global tmux user option:

```
tmux -L coop set -g @coop_arbiter_mode recommend
tmux -L coop show -gv @coop_arbiter_mode
```

Unset (or any value that is not `full`/`recommend`) reads as off. `coop hook`
and the hub poll each read it with one `show -gv`. If `#{@coop_arbiter_mode}`
turns out to resolve globally inside a pane format on tmux 3.4, the poll can get
it for free in `paneFormat` and drop the extra call — verify before relying on
it.

Turning the arbiter off is now an option write rather than a session kill, so
the y/esc confirm on `a`'s full → off is removed, as is `x`-on-arbiter's special
case.

### The judge

New `internal/hub/judge.go`:

- `buildJudgePrompt(...) string` — pure. Session name, trigger detail, screen
  (ANSI-stripped), last assistant message.
- `parseVerdict(string) (Verdict, error)` — pure. `Verdict{Action, Digit, Reason}`.
- `Judge(tm Tmux, tr *Transcripts, req JudgeReq) error` — resolves the pane,
  gathers context, calls an injected runner, parses, applies.

The `claude` invocation sits behind an injected `runner func(ctx, prompt) (string, error)`
so tests never shell out.

New `cmd/coop/judge.go` adds `coop judge <pane-id> [-detail ...]`, dispatched
pre-flag alongside `coop hook` — a subcommand that is another process calling in
must not pay for a TUI it will never draw.

### Invocation

```
claude -p \
  --model <arbiter.model> \
  --append-system-prompt "<preamble + policy>" \
  --output-format json
```

Prompt on stdin. No `--settings`, so no coop hooks are registered for the judge
itself.

**Working directory is the coop-owned arbiter dir, set explicitly — not
inherited.** `coop hook` runs with the *monitored session's* cwd, and `claude`
loads a `CLAUDE.md` from its working directory: inheriting it would pull
instructions out of the repo being judged straight into the judge's context,
which is the same untrusted source the preamble spends a paragraph fencing off.
`ArbiterHome` shrinks to creating that (empty) directory; the policy is still
read from `configDir/arbiter.md`, and the `.claude/settings.json` seed goes away
with the tools it was permitting.

Parsing is two layers: `--output-format json` wraps the turn, so take the
envelope's `result` text and then the first balanced `{…}` inside it — the model
may fence the JSON or wrap it in prose.

`arbiter.model` keeps its config key; **the default changes from `sonnet` to
`haiku`** (alias, resolving to `claude-haiku-4-5`). The judge is a single-shot
classification over a bounded prompt with a fixed output schema — the cheapest
tier that can follow the policy is the right one, and 200K context is far more
than one screen capture plus one assistant message needs.

## Flow

1. Monitored claude fires `PermissionRequest` / needs-input `Notification` →
   `coop hook`.
2. `ApplyHook` writes `waiting` as today.
3. `spawnJudge`:
   - read the global mode; bail if off
   - bail if the pane is a hub or already carries `@coop_arbiter_nudged`
   - set `@coop_arbiter_nudged`
   - spawn `coop judge <paneID> -detail "<NudgeDetail(p)>"` detached (`Setsid`),
     stdout/stderr to the judge log
   - on spawn failure, unset the marker
   - exit 0, silent, as the whole hook path does
4. `coop judge`:
   - resolve the pane; bail if it is no longer waiting (the human beat us to it)
   - `CapturePane` + `StripANSI`
   - last assistant message via the existing `Transcripts` path
   - build prompt, run under a deadline, parse
   - apply

### Applying a verdict

The prompt does **not** name the mode. The verdict is uniformly "what would you
do", and coop degrades it:

| mode | action | result |
|---|---|---|
| full | `answer` | `Answer` (which re-checks every gate) |
| full | `escalate` | `Note`, with the digit as `-suggest` if present |
| recommend | either | `Note`, with the digit as `-suggest` if present |

This is strictly better than the current arrangement, where the preamble tells
the arbiter the mode so it does not waste a turn on a refused action: recommend
mode now gets a suggestion it would otherwise not have produced, and the policy
prose drops its `(mode full)` qualifier.

`Answer` and `Note` become internal calls rather than a CLI surface. The gates
in `Answer` — full mode, not coop's own session, `pane_current_command` in the
allowlist, a dialog actually on screen — currently defend a CLI the model can
invoke with arbitrary argv. After this they defend a function whose only caller
is coop, with a digit the model produced as a JSON field. The "deliberately not
flags" property becomes structural rather than defensive.

## Failure modes

- **`claude` missing, non-zero exit, or deadline blown (60s)** → log, leave the
  row alone.
- **Malformed verdict** → log, no note. A row reading plain `waiting` is the
  honest fallback; "the arbiter broke" is noise on a row already being looked at.
- **`Answer` refuses** (screen changed, pane running a shell) → fall back to
  `Note` carrying the refusal reason. This is the preamble's step 3 today; it
  becomes `if err != nil`.
- **Env scrub, belt and braces.** The judge inherits `coop hook`'s environment,
  which carries the *monitored pane's* `TMUX_PANE`. Strip it, **and** run
  without `--settings`. Either alone would work; either alone is also a single
  point of failure that silently corrupts the status of the pane being judged.
- **Fan-out** is bounded by the per-pane `@coop_arbiter_nudged` marker — one
  judge per waiting pane, so the ceiling is simultaneously-waiting panes. No
  global cap in v1; if it bites, the cap goes in `spawnJudge`.
- **Orphan** — the session dies mid-judge; `findTarget` errors, logged, exit.

Diagnostics (prompts, raw verdicts, failures) go to
`$XDG_STATE_HOME/coop/judge.log`, appended, unrotated in v1.
`arbiter-audit.jsonl` stays a clean record of actions *taken*.

## What deletes

| Gone | Why |
|---|---|
| `LaunchArbiter`, `ArbiterCmd`, `shellQuote` | nothing to launch |
| `arbiterSettings` and the `.claude/settings.json` seed | the model runs no tools |
| `COOP_ALLOWED_CMDS` env plumbing | same |
| `ArbiterReady`, `arbiterReadyAge`, the +1s truncation fudge | no composer to race |
| `CatchupNudge` | nothing boots late |
| `HookNudge`, `NudgeText` | the prompt is built, not typed |
| `FindArbiter`, `Pane.Arbiter` | no arbiter pane exists |
| nav special-casing | `─ arbiter ───` divider, `arbiter · <mode>` label, `tab` skip, `SortPanes` pin, header-count exclusion |
| `a`'s full → off confirm, `x`-on-arbiter confirm | turning off is an option write |
| `coop answer`, `coop note` CLI verbs | `Answer`/`Note` become internal calls |

`coop peek` stays as a human debug aid.

## What survives unchanged

`sanitizeNote`, `noteMax`, `FormatArbiterLast` / `ParseArbiterLast`,
`ArbiterSuggestOf`, `NudgeDetail`, `RetireStaleEpisodes`, the
`@coop_arbiter_nudged` / `_note` / `_suggest` / `_last` markers, the `space`
applies-a-suggestion path and its hint chip, and `arbiter.md` as the user's
policy file. The untrusted-data framing in `arbiterPreamble` survives nearly
verbatim — it guards a prompt section instead of tool output.

Episode cleanup is unchanged: `ApplyHook` retires markers at the transition out
of waiting; `RetireStaleEpisodes` covers panes that publish no hook state.

The existing `~/.config/coop/arbiter/` directory and its `settings.json` go dead
but stay on disk — they are the user's files.

## Testing

- `buildJudgePrompt` — golden test, generic fixture paths only.
- `parseVerdict` — table test: bare JSON, fenced JSON, prose-wrapped, malformed,
  missing digit, non-digit digit, unknown action.
- `Judge` — injected runner + `fakeTmux`: full-mode answer, recommend downgrade
  to note-with-suggest, `Answer`-refused → note, unparseable → no-op, pane no
  longer waiting → no-op.
- `spawnJudge` — injected spawner: argv, marker set, marker unset on spawn
  failure, bail when mode is off.
- `scripts/e2e-smoke.sh` needs no change — it runs `-hooks=false` and never had
  an arbiter.

## Accepted gaps

- A pane with no hook state (started outside coop, or `-hooks=false`) never gets
  judged. Its stale notes and suggestions still retire via the poll.
- No global concurrency cap in v1.
- The judge log is unrotated.
