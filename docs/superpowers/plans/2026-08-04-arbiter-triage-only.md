# Arbiter Triage-Only Implementation Plan

> **For agentic workers:** Execute with the operator's personal `sdd` skill. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the arbiter's ability to send keystrokes, leaving it a triage summarizer whose verdict a human applies with `space`.

**Architecture:** `full` mode is deleted, which makes `hub.Answer` unreachable and cascades into three things that existed only to serve it: `JudgeReq.Allowed` (and its `arbiter.allowed_cmds` config key), the `@coop_arbiter_last` pane option, and the audit log's `"answered"` action. The judge prompt and `parseVerdict` are **unchanged** — an `answer` verdict already degrades to a note-with-suggestion in recommend mode, so removing the full-mode branch in `applyVerdict` is the whole behavioral change. `arbiter.model` defaults to `sonnet` because the note is now the product rather than a footnote.

**Tech Stack:** Go 1.x, Bubble Tea, tmux CLI. No new dependencies.

## Global Constraints

- **Never run git write commands.** No `git add`, `git commit`, `git checkout`, or worktrees. Edits land as uncommitted changes in the main tree. The operator handles git.
- **No developer paths in tests or fixtures.** No `/home/<username>/…`, no real home directory, no absolute path to this checkout. Use generic paths (`/home/user/sprocket-v2`) and `t.TempDir()` for anything actually written. Invent session and repo names (`alpha`, `beta`, `sprocket-v2`).
- **Unit tests never touch a real tmux.** `hub` tests use `fakeTmux` (`internal/hub/fake_tmux_test.go`) or parse canned output; `tui` tests construct a `Model` and feed `Update` messages.
- **TUI single-command invariant:** `Update` returns at most one command per message.
- **tmux writes are best-effort** with a comment saying so — a failure leaves state stale and the next poll self-heals.
- **Every task must leave the whole repo building.** Task order is load-bearing for this: the answer path is deleted *before* the mode constant it references, and every `internal/hub` deletion is paired in the same task with the `internal/tui` edit that keeps the TUI compiling. Do not reorder.
- Run `go build ./...` after each task in addition to the task's own gate.

---

### Task 1: Delete the answer path

`hub.Answer` is reachable only from `applyVerdict`'s full-mode branch. Deleting it here — before Task 2 touches the mode vocabulary — is what removes the `ArbiterModeFull` references that would otherwise break the build.

`JudgeReq.Allowed` is deliberately **left in place** and unused. An unused struct field is legal Go, and `cmd/coop/judgecli.go` still sets it; Task 4 removes the field and its producer together so no intermediate state is broken.

**Files:**
- Modify: `internal/hub/arbiter.go` — delete `AnswerReq` and `Answer` (`:206-336`), update `Note`'s doc comment (`:350-356`)
- Modify: `internal/hub/judge.go` — `Judge` (`:167-231`), `applyVerdict` (`:233-268`)
- Modify: `internal/hub/audit.go` — `AuditEntry` (`:11-24`)
- Test: `internal/hub/arbiter_test.go`, `internal/hub/judge_test.go`, `internal/hub/audit_test.go`, `internal/hub/fake_tmux_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `applyVerdict(tm Tmux, p Pane, v Verdict, req JudgeReq) error` — no `since`, no `mode`. `hub.Answer` and `hub.AnswerReq` no longer exist. `AuditEntry` no longer has `Digit`. `ArbiterModeFull` still exists (Task 2 removes it) but nothing outside `ArbiterMode`/`SetArbiterMode` references it.

- [ ] **Step 1: Write the failing test**

Read `internal/hub/fake_tmux_test.go` first and reuse its existing helpers and field names — do not add a second fake or invent field names. Adapt the identifiers below to match it. Add to `internal/hub/judge_test.go`:

```go
func TestApplyVerdictAnswerBecomesNote(t *testing.T) {
	// An "answer" verdict is still the strongest signal the model can
	// send — it means the policy clearly allowed this. It now lands as a
	// note carrying the digit as the suggestion space applies, exactly
	// as an escalation-with-digit does. Nothing sends a key.
	f := newFakeTmux()
	f.panes = []Pane{{ID: "%3", Session: "sprocket-v2"}}
	req := JudgeReq{
		Audit: filepath.Join(t.TempDir(), "audit.jsonl"),
		Now:   func() time.Time { return time.Unix(1700000000, 0) },
	}
	v := Verdict{Action: VerdictAnswer, Digit: "1", Reason: "runs the test suite"}

	if err := applyVerdict(f, f.panes[0], v, req); err != nil {
		t.Fatalf("applyVerdict: %v", err)
	}
	if got := f.paneOpts["%3/"+ArbiterNoteMarker]; got != "runs the test suite" {
		t.Fatalf("note = %q, want the verdict's reason", got)
	}
	if got := f.paneOpts["%3/"+ArbiterSuggestMarker]; got != "1" {
		t.Fatalf("suggest = %q, want %q", got, "1")
	}
	if len(f.sent) != 0 {
		t.Fatalf("keys sent: %v — the arbiter must never send", f.sent)
	}
}
```

The `"%3/"+Marker` key shape is what `judge_test.go:316` already uses for `paneOpts`; keep it. If the fake records sends under a different name than `sent`, use that.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/hub -run TestApplyVerdictAnswerBecomesNote -v`
Expected: compile error — `applyVerdict` currently takes `(tm, p, since, mode, v, req)`.

- [ ] **Step 3: Delete `AnswerReq` and `Answer`**

In `internal/hub/arbiter.go`, delete from the `// AnswerReq is one answer…` comment at `:206` through the closing brace of `Answer` at `:336`.

**Keep `digitRe`** (currently `:221`, inside the deleted range) — `ArbiterSuggestOf`, `Note`, and `parseVerdict` all use it. Move it above `findPaneTarget` before deleting the block.

**Keep `findPaneTarget`** — `Note` calls it.

**Remove the now-unused `slices` import.** `slices.Contains` at `:288` was `Answer`'s only use of it. Leave `time` (used by `NoteReq`) and `strconv` (used by `FormatArbiterLast`, which Task 3 removes).

- [ ] **Step 4: Update `Note`'s doc comment**

The phrase "Allowed in both modes" at `internal/hub/arbiter.go:352` describes a two-mode world. Replace the first sentence:

```go
// Note attaches an escalation note to the session's row — the arbiter's
// only write path to a monitored session, and not a keystroke. ApplyHook
// clears it at the status transition that ends the episode
// (RetireStaleEpisodes covers panes with no hook state).
```

- [ ] **Step 5: Simplify `applyVerdict`**

Replace `internal/hub/judge.go:233-268` entirely:

```go
// applyVerdict lands a verdict on the pane's row. Every verdict is a
// note: the arbiter has no send path, so an "answer" and an "escalate"
// differ only in how confident the model was, and both leave the digit
// as the suggestion the space key applies. The prompt still offers both
// actions on purpose — the model is asked what it would do, and coop
// decides what that is worth, which is one less thing the prompt can get
// wrong.
//
// It goes to p.ID, the pane Judge validated, never to p.Session: a
// session whose window is split has two panes sharing session_name, and
// the note would otherwise park on whichever tmux listed first.
func applyVerdict(tm Tmux, p Pane, v Verdict, req JudgeReq) error {
	warn, err := Note(tm, NoteReq{PaneID: p.ID, Text: v.Reason,
		Suggest: v.Digit, Audit: req.Audit, Now: req.now()})
	if warn != "" {
		req.logf("note on %s: %s", p.Session, warn)
	}
	return err
}
```

- [ ] **Step 6: Drop `since` and the `mode` local from `Judge`**

In `internal/hub/judge.go`, delete the `since := p.Claude.StatusSince` line and its comment (`:193-197`), and change the final call (`:230`) to:

```go
	return applyVerdict(tm, *p, v, req)
```

`mode` is now used only by the early return, so collapse `:168-171` to:

```go
	if ArbiterMode(tm) == ArbiterModeOff {
		return nil // turned off between the hook firing and this process starting
	}
```

Leave `JudgeReq.Allowed` alone — Task 4 removes it.

- [ ] **Step 7: Narrow `AuditEntry`**

In `internal/hub/audit.go`, delete the `Digit` field and retarget the `Action` comment:

```go
	Action  string    `json:"action"` // "escalated" — the only action left
	// Suggest is the digit an escalation offered for the human to apply.
	Suggest string    `json:"suggest,omitempty"`
```

Old log lines carrying `"action":"answered"` and `"digit"` still parse — coop never reads the audit back, and the fields are additive on disk.

- [ ] **Step 8: Verify nothing else called `Answer`**

Run: `grep -rn 'hub\.Answer\|AnswerReq' --include=*.go .`
Expected: no hits outside test files you are about to delete. A hit in `cmd/coop/` is a call site this plan missed — stop and report it.

- [ ] **Step 9: Run the gate**

Run: `go build ./... && go test ./internal/hub -v`
Expected: PASS. Delete tests whose entire subject was `Answer`'s gates — the allowlist refusal, the `NeedsInputScreen` refusal, the `since` mismatch refusal, and the full-mode refusal. `judge_test.go:254` has a table row asserting an `answer` verdict writes `ArbiterLastMarker`; change its expectation to `ArbiterNoteMarker`. `judge_test.go:316` reads `ParseArbiterLast` from `paneOpts` — that whole test is about the answered path; delete it. Keep every `Note` test.

---

### Task 2: Collapse the arbiter mode to on/off

Removes `ArbiterModeFull`. After this, a socket where an older coop wrote `@coop_arbiter_mode full` reads as **off** — the safe direction, consistent with "the mode that does nothing is the default, never the accident."

The TUI's `a` key is in this task, not a later one: `model.go:940` is the last surviving `ArbiterModeFull` reference once Task 1 has landed, so splitting it out would leave `internal/tui` non-building.

**Files:**
- Modify: `internal/hub/arbiter.go` — constants (`:18-22`), `ArbiterMode` (`:54-73`), `SetArbiterMode` (`:75-82`)
- Modify: `internal/tui/model.go` — the `a` key (`:933-945`), the `space` comment (`:946-950`), `arbiterHint`'s doc comment (`:1700-1704`)
- Test: `internal/hub/arbiter_test.go`, `internal/tui/model_test.go`

**Interfaces:**
- Consumes: Task 1's deletions (no `Answer`, no full-mode branch in `applyVerdict`).
- Produces: `ArbiterModeOff = "off"`, `ArbiterModeRecommend = "recommend"`. `ArbiterModeFull` no longer exists. `ArbiterMode` and `SetArbiterMode` keep their signatures.

- [ ] **Step 1: Write the failing tests**

Add to `internal/hub/arbiter_test.go` (adapt the fake's globals accessor to match `fake_tmux_test.go`):

```go
func TestArbiterModeFullReadsAsOff(t *testing.T) {
	// A socket written by an older coop still carries "full". It must
	// degrade to off, not to recommend: an unrecognized mode has always
	// meant "do nothing", and silently promoting it to the judging mode
	// would resurrect the setting this change removes.
	f := newFakeTmux()
	f.globals[ArbiterModeMarker] = "full"
	if got := ArbiterMode(f); got != ArbiterModeOff {
		t.Fatalf("ArbiterMode() = %q, want %q", got, ArbiterModeOff)
	}
}

func TestSetArbiterModeFullUnsets(t *testing.T) {
	f := newFakeTmux()
	f.globals[ArbiterModeMarker] = "recommend"
	if err := SetArbiterMode(f, "full"); err != nil {
		t.Fatalf("SetArbiterMode: %v", err)
	}
	if v, ok := f.globals[ArbiterModeMarker]; ok {
		t.Fatalf("marker still set to %q, want unset", v)
	}
}
```

Add to `internal/tui/model_test.go`, matching the file's existing style for building a `Model` and feeding keys — read a neighboring key test first:

```go
func TestArbiterKeyTogglesTwoWays(t *testing.T) {
	m := newTestModel(t)
	m.arbiterMode = hub.ArbiterModeOff

	m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.arbiterMode != hub.ArbiterModeRecommend {
		t.Fatalf("after one press: %q, want %q", m.arbiterMode, hub.ArbiterModeRecommend)
	}
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.arbiterMode != hub.ArbiterModeOff {
		t.Fatalf("after two presses: %q, want %q — there is no third mode",
			m.arbiterMode, hub.ArbiterModeOff)
	}
}
```

Use whichever entry point the neighboring tests use (`m.update` returning a concrete `Model`, or `m.Update` returning `tea.Model` with a type assertion) and whichever constructor they use instead of `newTestModel`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/hub -run 'ArbiterModeFull|SetArbiterModeFull' -v && go test ./internal/tui -run TestArbiterKeyTogglesTwoWays -v`
Expected: hub tests FAIL (`ArbiterMode` still recognizes `"full"`); tui test FAILs after two presses with `"full"`.

- [ ] **Step 3: Remove the constant and the branches**

In `internal/hub/arbiter.go`, replace the constant block at `:18-22`:

```go
// The arbiter is a triage judge: one headless claude per needs-input
// episode (see judge.go), whose verdict this package applies through
// Note. It never sends keystrokes — a verdict's digit is a suggestion
// the human applies with space, and the mode is only on or off.
const (
	ArbiterModeOff       = "off"       // no judging at all
	ArbiterModeRecommend = "recommend" // judge and annotate
)
```

Replace `ArbiterMode` at `:54-73`:

```go
// ArbiterMode reads the socket-global arbiter mode. It lives on the
// server's global option set rather than a session, because there is no
// arbiter session: every hub on the socket and every judge process coop
// hook spawns has to see the same value, and a judge runs outside any
// hub. Anything but an explicit recommend — unset, a read error, the
// "full" an older coop wrote, or a value a future version writes — reads
// as off: the mode that does nothing is the default, never the accident.
func ArbiterMode(tm GlobalReader) string {
	v, err := tm.GlobalOption(ArbiterModeMarker)
	if err != nil {
		return ArbiterModeOff
	}
	if v == ArbiterModeRecommend {
		return ArbiterModeRecommend
	}
	return ArbiterModeOff
}
```

Replace `SetArbiterMode`'s condition at `:78`:

```go
	if mode != ArbiterModeRecommend {
		return tm.UnsetGlobalOption(ArbiterModeMarker)
	}
```

- [ ] **Step 4: Replace the TUI's `a` key handler**

Replace `internal/tui/model.go:933-945`:

```go
	case "a":
		// Toggle off ↔ recommend. Mode is a socket-global tmux option, not
		// a session's existence, so turning it off is a write like any
		// other — no confirm. There is no third mode: the arbiter cannot
		// send keys, so the only question is whether it judges at all.
		next := hub.ArbiterModeRecommend
		if m.arbiterMode == hub.ArbiterModeRecommend {
			next = hub.ArbiterModeOff
		}
		m.arbiterMode = next // optimistic; the next poll is authoritative
		return m, m.arbiterModeCmd(next)
```

- [ ] **Step 5: Fix the `space` comment**

The comment at `internal/tui/model.go:946-950` says "Not gated on mode — a full-mode arbiter that escalated instead of answering still leaves a digit worth one key," describing a distinction that no longer exists. Replace it, leaving the code below unchanged:

```go
	case " ":
		// Apply the arbiter's suggestion: the same send the digit key
		// makes, minus reading the number out of the note. This is the
		// only path from a verdict to a keystroke, and a human presses
		// it — the judge itself can only annotate.
```

- [ ] **Step 6: Update `arbiterHint`'s doc comment**

Replace `internal/tui/model.go:1700-1701`:

```go
// arbiterHint is the a key's footer chip, doubling as the mode display:
// "a arbiter off|recommend".
```

The function body is unchanged — it already renders whatever `m.arbiterMode` holds.

- [ ] **Step 7: Run the gate**

Run: `go build ./... && go test ./internal/hub ./internal/tui -v`
Expected: PASS. Update or delete existing tests asserting a three-way cycle. `grep -rn 'ArbiterModeFull' --include=*.go .` must come back empty.

---

### Task 3: Delete the `@coop_arbiter_last` pane option

Written only by `Answer`, which Task 1 deleted. Removing it from `paneFormat` shifts every field index after it — that reindex is the highest-risk step in the plan.

The TUI's `arbiterDetail` edit is in this task because `model.go:1728` calls `hub.ParseArbiterLast(p.ArbiterLast)`; removing either side alone breaks `internal/tui`.

**Files:**
- Modify: `internal/hub/arbiter.go` — `ArbiterLast`, `FormatArbiterLast`, `ParseArbiterLast` (`:144-171`)
- Modify: `internal/hub/tmux.go` — `Pane.ArbiterLast` (`:79`), `ArbiterLastMarker` (`:146`), `paneFormat` (`:160`), `paneFields` (`:164`), field parsing (`:207-211`)
- Modify: `internal/tui/model.go` — `arbiterDetail` (`:1716-1735`)
- Test: `internal/hub/tmux_test.go`, `internal/hub/arbiter_test.go`, `internal/tui/model_test.go`

**Interfaces:**
- Consumes: Task 1's deletion of `Answer` (the only writer).
- Produces: `Pane` has no `ArbiterLast` field. `paneFields` is 18. `ArbiterLast`, `FormatArbiterLast`, `ParseArbiterLast`, and `ArbiterLastMarker` no longer exist.

- [ ] **Step 1: Write the failing test**

`internal/hub/tmux_test.go:92` already pads fixtures with `make([]string, paneFields-len(f))`, so most rows self-adjust. Add an explicit ordering test — a wrong *count* is caught by the length check, but only this catches a wrong *order*:

```go
func TestParsePanesClaudeFieldsShiftDown(t *testing.T) {
	// Removing @coop_arbiter_last shifts the claude-state fields down
	// one. A wrong index here misparses status into the wrong column
	// rather than erroring, so assert each one by name.
	f := []string{"alpha", "%1", "1234", "title", "0", "claude",
		"1700000000", "/home/user/sprocket-v2", "0", "0", "", "0",
		"note text", "2", "busy", "1700000100", "sess-id",
		"/home/user/sprocket-v2"}
	if len(f) != paneFields {
		t.Fatalf("fixture has %d fields, paneFields is %d", len(f), paneFields)
	}
	panes := parsePanes(strings.Join(f, `\037`) + "\n")
	if len(panes) != 1 {
		t.Fatalf("parsePanes returned %d panes, want 1", len(panes))
	}
	p := panes[0]
	if p.ArbiterNote != "note text" || p.ArbiterSuggest != "2" {
		t.Fatalf("arbiter fields = %q/%q", p.ArbiterNote, p.ArbiterSuggest)
	}
	if p.Claude == nil {
		t.Fatal("claude state is nil — the status field index is wrong")
	}
	if p.Claude.Status != "busy" || p.Claude.SessionID != "sess-id" ||
		p.Claude.CWD != "/home/user/sprocket-v2" {
		t.Fatalf("claude state = %+v", p.Claude)
	}
}
```

Use the same separator literal and the same parse entry point the neighboring tests in that file use — `\037` (the four-character `vis(3)` form) is what the existing helper emits, and `splitFields` is what parses it. Do not switch to a raw `\x1f`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/hub -run TestParsePanesClaudeFieldsShiftDown -v`
Expected: FAIL at the fixture length check — 18 fields, `paneFields` is 19.

- [ ] **Step 3: Delete the marker and the struct field**

In `internal/hub/tmux.go`: delete the `ArbiterLast string` field at `:79`, delete the `ArbiterLastMarker` constant at `:146`, and remove `\x1f#{" + ArbiterLastMarker + "}` from `paneFormat` at `:160`.

- [ ] **Step 4: Reindex the parser — the highest-risk step**

Change `paneFields` at `internal/hub/tmux.go:164` from 19 to 18, then rewrite `:207-211`:

```go
			ArbiterNote: f[12], ArbiterSuggest: f[13],
		}
		if f[14] != "" {
			p.Claude = &ClaudeState{Status: f[14], StatusSince: unixTime(f[15]),
				SessionID: f[16], CWD: f[17]}
```

Every index from 14 up shifts down by one. **Verify each index by reading `paneFormat` left to right and counting** — a wrong order misparses claude state into the wrong columns and every TUI status goes wrong with no error.

- [ ] **Step 5: Delete the format/parse helpers**

In `internal/hub/arbiter.go`, delete `ArbiterLast`, `FormatArbiterLast`, and `ParseArbiterLast` (`:144-171`). **Remove the now-unused `strconv` import** — `FormatInt`/`ParseInt` in these two functions were its only uses in the file.

- [ ] **Step 6: Delete the answered-line from `arbiterDetail`**

In `internal/tui/model.go`, remove the `hub.ParseArbiterLast` branch at `:1728-1731` and its `"answered %s by arbiter %s ago — %s"` format string. `arbiterDetail` keeps only the `p.ArbiterNote` branch and its empty fallback. If `fmt` or a duration helper becomes unused in the file as a result, remove that import too.

- [ ] **Step 7: Run the gate**

Run: `go build ./... && go test ./internal/hub ./internal/tui -v`
Expected: PASS. Hand-edit these assertions, which the padding helper cannot fix: `tmux_test.go:261-262` (asserts `p.ArbiterLast`), `arbiter_test.go:95-111` (`TestArbiterLastRoundTrip` — delete it), `arbiter_test.go:170` (reads `ParseArbiterLast` from `paneOpts`), `model_test.go:2575` (sets `m.panes[0].ArbiterLast`).

---

### Task 4: Remove the `arbiter.allowed_cmds` config key

Its only consumer was `Answer`, deleted in Task 1. This task removes the now-unused `JudgeReq.Allowed` field together with its producer, so no intermediate state is broken.

`hub.DefaultAllowedCmds` **stays** — the hub's `-allowed-cmds` flag still uses it for the TUI's own digit keys.

**Files:**
- Modify: `internal/config/config.go` — `Arbiter` struct (`:23-34`)
- Modify: `internal/hub/judge.go` — delete `JudgeReq.Allowed` (`:138`)
- Modify: `cmd/coop/judgecli.go` — call site (`:84`), `JudgeReq` literal (`:91`), `judgeConfig` (`:104-137`), delete `judgeAllowedCmds` (`:139-151`)
- Test: `internal/config/config_test.go`, `cmd/coop/judgecli_test.go`

**Interfaces:**
- Consumes: Task 1's deletion of `Answer`.
- Produces: `judgeConfig(cfgPath string, logf func(string, ...any)) string` — returns the model only. `judgeAllowedCmds` no longer exists. `config.Config.Arbiter` has only `Model`. `JudgeReq` has no `Allowed`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/coop/judgecli_test.go`:

```go
func TestJudgeConfigUnparseableFileUsesDefaultModel(t *testing.T) {
	// An unparseable config used to mean "no send gate". There is no
	// gate any more — the judge cannot send — so a broken file costs
	// only the configured model, and that is logged rather than silent.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logged string
	got := judgeConfig(path, func(f string, a ...any) { logged = fmt.Sprintf(f, a...) })
	if got != hub.DefaultArbiterModel {
		t.Fatalf("model = %q, want %q", got, hub.DefaultArbiterModel)
	}
	if logged == "" {
		t.Fatal("an unparseable config must be logged")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/coop -run TestJudgeConfigUnparseableFileUsesDefaultModel -v`
Expected: compile error — `judgeConfig` returns two values.

- [ ] **Step 3: Shrink the config struct**

In `internal/config/config.go`, replace `:23-34`:

```go
	// Arbiter configures the a-key triage judge.
	Arbiter struct {
		Model string `json:"model"` // claude model id/alias; "" = sonnet
	} `json:"arbiter"`
```

An `allowed_cmds` key left in a user's `config.json` becomes an unknown key. `AddRepo` rewrites the file from its own raw JSON key by key, so it survives untouched rather than being silently dropped — harmless, and Task 6's CLAUDE.md rewrite says it is no longer read.

- [ ] **Step 4: Delete `JudgeReq.Allowed`**

In `internal/hub/judge.go`, delete the `Allowed []string` field and its comment at `:138`.

- [ ] **Step 5: Rewrite `judgeConfig` and delete `judgeAllowedCmds`**

Replace `cmd/coop/judgecli.go:104-151` with:

```go
// judgeConfig resolves the model from the config file. An unparseable
// file is not a missing one — it is logged, because the operator wrote
// something they believed was in effect. It is no longer a safety
// question: the judge has no send path, so the worst a broken file costs
// is a judgement from the default model.
//
// The path is config.DefaultPath() and nothing else — see the comment at
// the call site for why a flag, an environment variable, and a tmux
// option were all rejected.
func judgeConfig(cfgPath string, logf func(string, ...any)) string {
	c, err := config.Load(cfgPath)
	switch {
	case err == nil:
		if c.Arbiter.Model != "" {
			return c.Arbiter.Model
		}
	case !os.IsNotExist(err):
		logf("config: %v: judging with the default model", err)
	}
	return hub.DefaultArbiterModel
}
```

Update the call site at `:84` to `model := judgeConfig(cfgPath, logf)` and delete `Allowed: allowed,` from the `JudgeReq` literal at `:91`.

- [ ] **Step 6: Check whether `splitCmds` still has a caller**

Run: `grep -rn 'splitCmds' --include=*.go .`

`cmd/coop/main.go` should still call it for the `-allowed-cmds` flag — if so, leave it. If this task removed its last caller, delete it and say so in the task report.

- [ ] **Step 7: Run the gate**

Run: `go build ./... && go test ./cmd/coop ./internal/config ./internal/hub -v`
Expected: PASS. Delete `config_test.go` cases asserting `AllowedCmds` round-trips, and `judgecli_test.go` cases covering `judgeAllowedCmds`' three-way fallback — those behaviors no longer exist.

---

### Task 5: Default `arbiter.model` to sonnet

**Files:**
- Modify: `internal/hub/judgeexec.go:16-20`
- Test: `internal/hub/judgeexec_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `hub.DefaultArbiterModel = "sonnet"`.

- [ ] **Step 1: Write the failing test**

Add to `internal/hub/judgeexec_test.go`:

```go
func TestDefaultArbiterModelIsSonnet(t *testing.T) {
	if DefaultArbiterModel != "sonnet" {
		t.Fatalf("DefaultArbiterModel = %q, want %q", DefaultArbiterModel, "sonnet")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/hub -run TestDefaultArbiterModelIsSonnet -v`
Expected: FAIL — `DefaultArbiterModel = "haiku", want "sonnet"`.

- [ ] **Step 3: Change the constant and its rationale**

Replace `internal/hub/judgeexec.go:16-20`:

```go
// DefaultArbiterModel is what arbiter.model defaults to. It was haiku
// while a verdict could send a keystroke and the classification was the
// product. It is not any more: the arbiter only annotates, so the note a
// human reads on the row — and the digit they apply with one key — *is*
// the product, formed by summarizing a captured terminal that an
// attacker can influence. The whole per-episode cost is a bounded prompt
// and a one-line verdict, so the better model is worth it; an operator
// who disagrees sets arbiter.model.
const DefaultArbiterModel = "sonnet"
```

- [ ] **Step 4: Run the gate**

Run: `go build ./... && go test ./internal/hub -run 'Judge|ArbiterModel' -v`
Expected: PASS. Update any test asserting the literal `"haiku"`.

---

### Task 6: Rewrite the arbiter documentation and the policy seed

The prose is load-bearing in this repo — a stale rationale reads as a live one. No code test; verification is that the described behavior matches the code.

**Files:**
- Modify: `CLAUDE.md` — the "The arbiter" section
- Modify: `internal/hub/arbiter.go` — `arbiterPolicySeed` (`:424-447`)

**Interfaces:**
- Consumes: everything from Tasks 1–5.
- Produces: documentation only.

- [ ] **Step 1: Fix the policy seed**

In `internal/hub/arbiter.go`, `arbiterPolicySeed` contains `## Fine to approve (mode full)`. Replace that heading and the "Escalate, don't answer" heading below it:

```
## Safe to suggest
- running the project's tests, linters, builds, or read-only commands

## Note without a suggestion
- file edits and writes — a benign-looking diff still needs human eyes
- anything this policy does not name
```

Also change the opening line from "When unsure, ALWAYS escalate with a note instead of answering" to "When unsure, omit the digit — a note with no suggestion is always safe."

`ArbiterHome` seeds `arbiter.md` **once** and never overwrites, so an existing user's policy file is deliberately untouched. Note that in the task report: operators who already have an `arbiter.md` keep their `(mode full)` heading, which is harmless prose to a model that can no longer answer.

- [ ] **Step 2: Rewrite the CLAUDE.md arbiter section**

Rewrite it to describe what the code now does. It must state:

- `a` toggles off ↔ recommend. No confirm, no third mode.
- `ArbiterMode` reads anything but `recommend` as off, **including the `full` an older coop wrote** — the safe direction.
- Judging is still one headless `claude` per needs-input episode, spawned by `coop hook` via `ClaimEpisode`. That path is **unchanged** — keep the existing prose about the `O_EXCL` claim, `claimRoot` ignoring `$XDG_STATE_HOME`, `setsid`, and "no leader election and no hub required."
- The judge's cwd, `JudgeEnv`, the constructed `PATH`, and the no-`--settings` rule are **unchanged**. Keep that prose.
- **The verdict is now always a note.** `parseVerdict` still validates both actions and the prompt still offers both — the model is asked what it would do, and coop decides what that is worth. An `answer` verdict differs from an `escalate` only in confidence; both leave the digit as the `space` suggestion.
- **`Answer` is gone.** The five gates it carried — full mode, not-a-coop-session, the `pane_current_command` allowlist, `NeedsInputScreen`, and the `@coop_status_since` re-check — are deleted with it, because there is no send path left to gate. Say this explicitly; those gates have their own paragraphs today and a reader will look for them.
- `space apply N` sends through the TUI's own `answerCmd`, which re-checks `pane_current_command` against the hub's `-allowed-cmds` at send time. That has always been the path and is unchanged.
- **`arbiter.allowed_cmds` no longer exists.** Delete the paragraph explaining why it is read from `config.json` and nowhere else — but keep the identical reasoning where it still applies, to `config.DefaultPath()` and `arbiter.md`.
- `@coop_arbiter_last` is gone; the row's arbiter line is the note only.
- `arbiter.model` defaults to **sonnet**, and say why: the note is the product now, not a footnote to a keystroke.
- The audit log records escalations only.
- Keep all four accepted gaps — they still hold.

Delete, do not soften, the paragraph beginning "**None of this is a security boundary against a hostile session.**" and replace it with one true of the new design: coop and the sessions it watches still run as the same user on the same tmux server, and a session with code execution can still set `@coop_arbiter_mode`, park notes on any row, and rewrite `config.json` and `arbiter.md`. What changed is that a misfiring judge can no longer answer a dialog — the worst an unattended arbiter now does is put wrong text on a row.

Also update the **`### The stat column`** section only if it mentions `paneFormat`'s field count, and the **tmux targeting gotchas** section only if it names `ArbiterLastMarker`. Leave them otherwise.

- [ ] **Step 3: Verify the docs against the code**

Run: `grep -n 'mode full\|ArbiterModeFull\|hub\.Answer\|allowed_cmds\|arbiter_last' CLAUDE.md`
Expected: no surviving reference to full mode, `hub.Answer`, `arbiter.allowed_cmds`, or `@coop_arbiter_last`. Matches on `-allowed-cmds` (the hub flag) and `DefaultAllowedCmds` are correct and must stay.

- [ ] **Step 4: Full build and test**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Real-tmux smoke**

Run: `scripts/e2e-smoke.sh`
Expected: PASS. It runs with `-hooks=false` and exercises no arbiter path — but Task 3 changed `paneFormat` and the field indices, which is exactly the plumbing this script covers. A failure here means the reindex is wrong.
