# Hook-driven arbiter nudges

**Date:** 2026-07-30
**Status:** Approved design, not yet implemented
**Depends on:** `2026-07-30-hook-status-tier-design.md` (the `coop hook`
subcommand and its status writes must exist first)

## Problem

`ArbiterNudger.Apply` runs off the hub's 1-second poll, and that placement
is the source of its two ugliest mechanisms: the `leads` election (only the
lexicographically first hub nudges, because the nudged marker is not a
test-and-set) and the `@coop_arbiter_nudged` dedupe against N hubs seeing
the same needs-input episode. It also means nudges stop when no hub is
attached, arrive up to a second late, and carry only a session name — the
arbiter must `peek` before it can decide anything.

Once `coop hook` exists, the needs-input *event* fires exactly once, in
exactly one process, per dialog. That is where the nudge belongs.

## Decision summary

- `coop hook` sends the nudge itself when it writes `waiting`: on
  `PermissionRequest` and on `Notification` types `permission_prompt`,
  `elicitation_dialog`, `agent_needs_input`.
- `ArbiterNudger`, `NewArbiterNudger`, `Apply`, `leads`, and the
  `@coop_arbiter_seen` marker are deleted, along with the Model's `nudge`
  field and its poll-time call. `NudgeText` stays.
- One hub-side remnant: the hub that launches the arbiter performs a
  **one-shot catch-up** on its next poll tick, nudging panes already in
  needs-input. No election — exactly one hub launches.
- Nudges keep working with zero hubs attached: detach every TUI and the
  arbiter still triages.
- The nudge text is enriched from the hook payload (`tool_name`,
  `tool_input` / `permission_rule`), so the arbiter can often decide
  without a `peek` round-trip.

## Hook-side nudge (`coop hook`)

On a waiting-type event, after writing `@coop_claude_status`:

1. **Find the arbiter**: list sessions on the socket, take the one with
   `@coop_arbiter=1`; read `@coop_arbiter_mode` (absent/anything but
   `full` reads as `recommend`, per `ArbiterModeOf`). No arbiter → do
   nothing, and do **not** set the nudged marker (a later arbiter's launch
   catch-up must still find this pane un-nudged).
2. **Skip self**: if the hook's own session is the arbiter (its
   `ArbiterCmd` carries `--settings` too), do nothing — the arbiter is not
   nudged about its own dialogs, same as today's `!p.Arbiter` guard.
3. **Age gate**: skip if the arbiter session is younger than ~1s
   (`#{session_created}`), replacing the `@coop_arbiter_seen` swallow-gate
   — keys typed at a freshly launched claude are eaten. The launch
   catch-up covers this window.
4. **Dedupe**: skip if this pane already has `@coop_arbiter_nudged`.
   Set the marker, then `send-keys` the nudge to the arbiter pane.
   Hooks are synchronous, so the two events a single dialog may fire
   (`PermissionRequest` then `Notification permission_prompt` — co-firing
   to be confirmed in the status-tier spike) run sequentially in the
   session's process: no concurrent writers per pane.

**Marker lifecycle** (all in `coop hook`, single writer per pane): set on
nudge; cleared whenever the hook writes status `busy` or `idle` — the
episode is over. `SessionEnd` unsets it with the other options; a dead
pane takes it along regardless.

**Nudge text**: `NudgeText` gains the trigger detail, one line, sanitized
via `sanitizeNote`, total length capped (~200 chars):

    coop: session "alpha" needs input (mode: recommend; Bash: npm test)
    coop: session "beta" needs input (mode: full; question)

`PermissionRequest` contributes `tool_name` plus a truncated one-line
rendering of the most useful `tool_input` field (`command` for Bash, else
`permission_rule`); `Notification` triggers contribute a short word for
their type (`question` for `elicitation_dialog`, etc.). Catch-up nudges
(below) have no payload and keep today's plain form.

## Launch catch-up (hub side)

An arbiter launched while sessions already sit in needs-input gets no
events for them — they fired before it existed. The hub that ran
`arbiterCreateCmd` sets a flag; on the next poll tick where the arbiter
appears in the pane list (≥1 tick after creation, so past the key-swallow
window), it nudges every pane whose `@coop_claude_status` is `waiting` and
that lacks `@coop_arbiter_nudged`, sets their markers, and clears the
flag. Gating on the hook-published option, not derived `StatusNeedsInput`,
keeps title-tier panes out: a non-hook pane must never acquire a marker,
because no hook will ever clear it. Plain
`NudgeText`, no election, no persistent state, no per-poll work.

Accepted gap: a dialog that fires during the ~1s age-gate window *after*
the catch-up ran is nudged by neither path. The row still shows NEEDS
INPUT in every hub; the arbiter just doesn't hear about that one episode.

## Deletions

- `ArbiterNudger` struct, `NewArbiterNudger`, `Apply`, `leads`
- `ArbiterSeenMarker` and the `ArbiterSeen` field wherever parsed
- Model `nudge` field and the `nudge.Apply(panes, hubs)` poll call (and
  the `hubs` plumbing if nothing else consumes it)
- Their tests, replaced per below

## What does not change

- `Answer`'s call-time gates, the audit log, `coop peek|note`, the
  suggest/`space` flow, mode cycling, and the arbiter's pinned-last row.
- Nudge transport: still `send-keys` of a one-line message into the
  arbiter pane.
- Non-hook sessions (hand-started) never nudge — accepted per operator;
  they surface through the title tier's NEEDS INPUT only.

## Risks and open items

1. **Co-firing** of `PermissionRequest` and `Notification` for one dialog:
   confirm in the status-tier spike (log event sequences). The marker
   dedupes regardless; this only decides whether the second event is
   common enough to matter for nudge-text choice (prefer the
   `PermissionRequest` payload when both fire — it is richer and fires
   first).
2. **Arbiter mid-turn when a nudge lands**: unchanged from today —
   send-keys queues input into the running claude.
3. **Two panes' hooks nudging concurrently** (different sessions): two
   send-keys interleave at the arbiter, each a complete line + Enter, same
   as today's sequential-hub behavior. Harmless.

## Testing

- **Unit, `coop hook`** (fake executor, table-driven): no arbiter → no
  marker, no keys; arbiter present → marker set + nudge sent; young
  arbiter → skipped, marker unset; own-session-is-arbiter → skipped;
  second waiting event → deduped; marker cleared on busy/idle writes;
  text enrichment and truncation per event type. Generic fixture names
  (`alpha`, `sprocket-v2`) per repo test policy.
- **Unit, tui**: launch flag set by `arbiterCreateCmd` success; catch-up
  fires once on the first poll containing the arbiter, nudges only
  un-marked needs-input panes, clears the flag.
- **e2e**: `scripts/e2e-smoke.sh` for regressions; real-event nudging is
  covered by the status-tier spike's manual pass.

## Docs

- CLAUDE.md arbiter section: nudges originate in `coop hook`
  (event-driven, hub-optional); `leads`/`@coop_arbiter_seen` paragraphs
  drop; launch catch-up documented as the one hub-side piece.
