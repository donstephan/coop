# coop-sdd: sdd as a first-class citizen — design

Date: 2026-08-04
Status: approved design, no plan yet

## What this is

coop grows from monitoring Claude Code sessions to shipping the methodology
they run: the operator's `sdd` skill (subagent-driven development against a
declared file manifest) becomes a coop-delivered skill, its runs live under
`.coop/sdd/` in the repo, its progress shows in the nav as a sub-row under
the session running it, and the arbiter's judge sees the run's declared
manifest when weighing a permission request.

This is deliberate scope: coop is opinionated about how development happens,
not a neutral hub. The toolbox set the pattern — the repo declares, coop
provisions, sessions consume — and sdd runs are the same shape one level up.

## The invariant: observe, never drive

Everything trustworthy about coop follows from one property: killing coop
mid-anything changes nothing. This feature keeps it, and future features are
measured against it the way "there is no Answer" gates the arbiter:

- `coop sdd state` is **write-only decoration**. coop never reads it back to
  sequence, gate, or dispatch anything in the run.
- A skill that never publishes still works; it just shows nothing.
- The moment coop dispatches a task or gates a wave, it has become a daemon
  with extra steps. That is the line, and it stays uncrossed.

## Skill delivery

- The skill is named **`/coop-sdd`** and lives at
  `~/.claude/skills/coop-sdd/SKILL.md`. Plain skill directory — no plugin
  machinery. The `/coop:sdd` colon form needs marketplace registration in
  Claude Code's undocumented internal registry files
  (`known_marketplaces.json`, `installed_plugins.json`), which is the
  `sessions/<pid>.json` mistake again; rejected.
- The skill text is `//go:embed`ed in the binary
  (`internal/hub/skills/coop-sdd/SKILL.md`) and **rewritten on every hub
  launch**, exactly as `WriteHookSettings` writes `hooks-settings.json`:
  regenerated so it can never lag the binary. Editing the skill means
  rebuilding coop; that coupling is what buys never-stale.
- Gated by `-skill=false` / `COOP_SKILL=0`. `scripts/e2e-smoke.sh` must set
  it — an e2e run must never clobber the real skill file.
- The operator's hand-maintained `~/.claude/skills/sdd/` is **untouched**.
  It survives as a fallback and coop never writes to it.
- The embedded text is the current sdd skill plus the changes in "Skill text
  changes" below.

## Run layout: `.coop/sdd/<slug>/`

Runs move from `.sdd/` to `.coop/sdd/`, joining `tools.Dockerfile` and
`toolbox.json` as the repo's committed coop surface. The run dir splits the
two jobs `run.md` used to do (claim and record), because finishing deleted
the record along with the claim:

| File | Tracked in git | Role |
|---|---|---|
| `run.md` | **yes** | durable record: plan path, date, `## Manifest`, outcome. Survives the run. |
| `claim.md` | no | presence = "this run is active". Written at setup, deleted at Finishing. The overlap check reads sibling `claim.md`s, not `run.md`s. |
| `base/`, `review.md` | no | before-state snapshots and the review package — bulky (a real `review.md` here is 112 KB) and redundant once the work is committed. |

`.coop/sdd/.gitignore` holds `claim.md`, `base/` and `review.md` patterns
instead of today's self-ignoring `*`. Existing `.sdd/` dirs are legacy; the
skill's "leftover from an older version" clause reports them and offers
deletion, never resumes them.

Setup reads an existing `.coop/sdd/<slug>/` as:

- **`claim.md` present** — an unfinished run of this plan. Ask the operator:
  resume (recreate native tasks from the plan, judge done-ness from the
  current tree, never re-snapshot `base/`) or discard (delete the slug's
  folder, start over).
- **`run.md` only, no `claim.md`** — a finished prior run of this plan.
  Delete its stale `base/` and `review.md`, then start fresh: the new run
  **overwrites** `run.md`. Git history preserves the old record — that is
  exactly why `run.md` is tracked, so no history lives in the working
  file.

## Publishing: `coop sdd state` / `coop sdd clear`

One verb, whole state per call, so a missed call self-heals at the next:

```
coop sdd state -run .coop/sdd/<slug>/run.md -task 3 -total 7 -phase impl
coop sdd clear
```

- Behaves like `coop hook`: silent, always exit 0, best-effort. A failure is
  one stale sub-row, never a wedged run.
- Parses `run.md`'s `## Manifest` section itself rather than taking paths as
  flags — that format is a contract coop now owns both ends of.
- Phases: `setup`, `impl` (with `-task`/`-total`), `review`.
- Writes pane user options on `$TMUX_PANE`, dying with the pane:

| Option | Content | Read by |
|---|---|---|
| `@coop_sdd_slug` | run slug | poll, via `paneFormat` |
| `@coop_sdd_task` | `3/7` | poll, via `paneFormat` |
| `@coop_sdd_phase` | `setup`/`impl`/`review` | poll, via `paneFormat` |
| `@coop_sdd_manifest` | newline-joined declared paths | `coop judge` only, explicit `show -pv` once per episode — keeps a 30-path list out of the 1-second poll |
| `@coop_sdd_run` | path to `run.md` | the human |

- Retired by `coop sdd clear` at Finishing, and by `ApplyHook` on
  `SessionEnd` so a killed session leaves nothing behind.
- **Accepted gap:** a run abandoned without either event leaves a stale
  sub-row until the pane dies. The poll has no way to know — same class as
  the arbiter's accepted gaps.
- Blast radius of the new CLI verb: it decorates its own pane and nothing
  else — the same radius `coop hook` already has. Not a security boundary,
  per the standing caveat: any session on the socket can already write any
  option.

## TUI: the sub-row

Any row whose pane publishes `@coop_sdd_slug` gets an indented, dimmed
sub-row:

```
▸ ● claude — coop                    12m    45k
    └ sdd 2026-08-04-toolbox  3/7
  ● claude — sprocket-v2             3m     8k
```

- Right-hand text from phase: `setup`, `3/7`, `review`. Slug truncates to
  fit `navWidth`.
- Sub-rows are **not selectable**: `j`/`k` move by pane; a click on a
  sub-row selects its parent session.
- `viewNav` and `paneAt` currently walk the same implicit one-line-per-pane
  layout by hand; a second row kind breaks that. Both move onto a shared
  `navLayout() []navLine{kind, paneID}` with kinds `repoHeader`, `pane`,
  `sddSub` — the same single-source-of-geometry fix `pickerLayout` already
  is for the picker.

## The judge sees the manifest

- `coop judge` reads `@coop_sdd_manifest` (and slug/task/phase) with an
  explicit `show -pv`, once per episode.
- `buildJudgePrompt` gains a labelled section: slug, task `k/n`, phase, and
  the declared file list. Framed as coop-published state, **still
  untrusted** — the skill publishes it from inside the monitored pane. The
  slug goes through `sanitizeNote` and renders `%q` exactly like
  `agent_type`; the path list is length-capped with an explicit "… N more"
  so truncation is never silent.
- `arbiterPreamble` gains one sentence: a request touching a file outside
  the declared manifest is a scope concern worth surfacing in the note.
  The model weighs it; coop computes nothing — no dialog-parsing, no
  in/out-of-manifest pre-judgement (a wrong parse would be a confident
  wrong fact).
- The `judge.log` episode line gains `sdd=<slug>:<task>` (or `sdd=` when
  none), so a scope escalation is distinguishable from an ordinary one.

## Skill text changes (embedded copy vs today's sdd)

1. All paths `.sdd/` → `.coop/sdd/`.
2. Setup writes `claim.md` (the claim) and `run.md` (the tracked record);
   overlap detection reads sibling `claim.md`s. Finishing deletes
   `claim.md` only and appends the outcome to `run.md`. The resume/discard
   and finished-prior-run rules key on `claim.md` as described under "Run
   layout" above.
3. `.gitignore` covers `claim.md`, `base/`, `review.md` — `run.md` is
   committed by the operator with the work.
4. Publish calls: `coop sdd state` at setup, at each task boundary, and at
   review start; `coop sdd clear` at Finishing.
5. A red flag: the publish is a reporting side-channel coop never reads
   back; if `coop sdd` fails, continue — never block on it, never retry it
   in a loop.
6. The legacy-leftovers clause covers `.sdd/` dirs.

## Testing

- **hub**: `## Manifest` parsing (generic fixture paths only, per the
  testing rules); publish/clear against `fakeTmux`; `SessionEnd`
  retirement; judge prompt construction with a hostile slug and hostile
  manifest paths (heading-close attempts, control characters).
- **tui**: table test walking every `y` of a rendered nav asserting
  `paneAt(y)` agrees with what `viewNav` drew — the contract the sub-row
  endangers. Sub-row rendering per phase; click-on-sub-row selects parent.
- **e2e**: unchanged except passing `-skill=false`.

## Accepted costs

1. Skill text and binary co-evolve; editing methodology prose means
   rebuilding coop. Deliberate — the delivery contract
   (`coop sdd state`/`clear` + the `.coop/sdd` layout) is a clean seam, so
   delivery could retreat to "coop documents the contract" later without
   touching sub-row, judge, or layout.
2. Stale sub-row after an abandoned run, until the pane dies.
3. `coop sdd` is a verb a model can reach for; radius is own-pane
   decoration.
4. Cross-machine claim collisions stay unsolved; a tracked `run.md` at
   least carries the record into clones.

## Out of scope

- coop sequencing, gating, or dispatching any part of a run (the
  invariant).
- Parsing permission dialogs to pre-compute in/out-of-manifest.
- Plugin-style `/coop:sdd` namespacing.
- Migrating existing `.sdd/` run dirs (reported as legacy, not converted).
- A generic native-task progress indicator for every session, derived from
  `PostToolUse` events on `TaskCreate`/`TaskUpdate` (documented payload
  fields, no file parsing). A different feature with a lower accuracy bar
  — drift-tolerant decoration, not run identity — that would compose with
  this one: generic task garnish for all sessions, sdd identity from the
  push. Deliberately not here: hook-side counting needs racy
  read-modify-write pane options, and the events cannot name a slug or
  manifest anyway.
