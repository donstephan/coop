# Hook-Driven Arbiter Nudges Implementation Plan

> **For agentic workers:** This plan is executed with the operator's `sdd`
> skill (fresh implementer subagent per task, one whole-branch review at the
> end). **Never run git commands** — no commits, no add, nothing; the
> operator handles git. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move arbiter nudging from the hub's 1-second poll into `coop hook`,
so nudges are event-driven, hub-optional, and enriched with the trigger's
tool details — deleting the `leads` election and the seen-marker gate.

**Architecture:** `coop hook` already runs on every waiting-type event; after
writing status it now also finds the arbiter on the socket (one
`ListSessions`), age-gates it, dedupes via `@coop_arbiter_nudged`, and
`send-keys` an enriched `NudgeText` line. `ApplyHook` retires the episode
markers (nudged/note/suggest) when status leaves waiting — the cleanup
`ArbiterNudger.Apply` used to do. The one hub-side remnant is a launch
catch-up: the hub that launched the arbiter nudges already-waiting panes
once, from inside the poll closure. `ArbiterNudger`, `leads`, and
`@coop_arbiter_seen` are deleted.

**Tech Stack:** Go, tmux CLI, Claude Code hook payloads.

**Spec:** `docs/superpowers/specs/2026-07-30-hook-driven-nudges-design.md`
**Depends on:** the hook status tier (already in the working tree —
`internal/hub/hook.go`, `cmd/coop/hookcli.go` exist).

## Global Constraints

- **No git commands, ever** — the operator handles all git.
- **No real paths or names in tests/fixtures**: `/home/user/alpha`, sessions
  `alpha`/`beta`/`sprocket-v2`; disk writes under `t.TempDir()`.
- **Never split `-F` output with `strings.Split` on the raw byte** — always
  `splitFields` (tmux ≤ 3.4 vis(3)-escapes `\x1f` to `\037`).
- **tui single-command invariant**: `Update` returns at most one command per
  message; tmux I/O inside `tea.Cmd`/poll closures capturing by value.
- **Best-effort writes** get a comment saying so; failures self-heal on a
  later event or poll.
- Nudge text is typed into a live claude pane: every dynamic part MUST pass
  `sanitizeNote` (control bytes are a terminal-injection vector; a newline
  would submit early).
- The age gate is 1 second — twice the measured 0.5s key-swallow threshold
  documented in the deleted `Apply` (2.1.220: sends at 0.45s stick, 0.5s go
  through). Preserve that measurement in a comment wherever the gate lives.
- After each task run the tests named in it; full `go test ./...` where
  stated. `internal/tui/model.go`/`model_test.go` carry unrelated
  uncommitted operator edits — surgical changes only.

---

### Task 1: Enriched nudge text — `NudgeText` detail + `NudgeDetail`

**Files:**
- Modify: `internal/hub/arbiter.go` (`NudgeText` ~line 59; callers: `Apply`
  ~line 205 passes `""` for now)
- Modify: `internal/hub/hook.go` (`HookPayload` fields)
- Test: `internal/hub/arbiter_test.go`, `internal/hub/hook_test.go`

**Interfaces:**
- Produces: `NudgeText(session, mode, detail string) string` (detail `""`
  reproduces today's exact output); `NudgeDetail(p HookPayload) string`;
  `HookPayload` gains `ToolName`, `PermissionRule`, and
  `ToolInput struct { Command string }`. Tasks 2–3 rely on these names.

- [ ] **Step 1: Write the failing tests**

Add to `internal/hub/arbiter_test.go`:

```go
func TestNudgeTextDetail(t *testing.T) {
	if got := NudgeText("alpha", "recommend", ""); got != `coop: session "alpha" needs input (mode: recommend)` {
		t.Errorf("plain form changed: %q", got)
	}
	if got := NudgeText("alpha", "full", "Bash: npm test"); got != `coop: session "alpha" needs input (mode: full; Bash: npm test)` {
		t.Errorf("detail form: %q", got)
	}
	// Control bytes and newlines in the detail are typed into a live
	// claude pane — they must be flattened, and the whole line capped.
	got := NudgeText("alpha", "full", "Bash: rm\n-rf \x1b[31m"+strings.Repeat("x", 400))
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("detail not sanitized: %q", got)
	}
	if n := len([]rune(got)); n > 220 {
		t.Errorf("nudge line %d runes, want ≤220", n)
	}
}
```

Add to `internal/hub/hook_test.go`:

```go
func TestNudgeDetail(t *testing.T) {
	cases := []struct {
		name string
		p    HookPayload
		want string
	}{
		{"bash command", HookPayload{Event: "PermissionRequest", ToolName: "Bash",
			ToolInput: toolInput("npm test"), PermissionRule: "Bash(npm *)"}, "Bash: npm test"},
		{"rule fallback", HookPayload{Event: "PermissionRequest", ToolName: "WebFetch",
			PermissionRule: "WebFetch(domain:example.com)"}, "WebFetch: WebFetch(domain:example.com)"},
		{"bare tool", HookPayload{Event: "PermissionRequest", ToolName: "Edit"}, "Edit"},
		{"no payload", HookPayload{Event: "PermissionRequest"}, ""},
		{"question", HookPayload{Event: "Notification", NotificationType: "elicitation_dialog"}, "question"},
		{"agent question", HookPayload{Event: "Notification", NotificationType: "agent_needs_input"}, "agent question"},
		{"permission notify", HookPayload{Event: "Notification", NotificationType: "permission_prompt"}, "permission"},
		{"non-waiting event", HookPayload{Event: "Stop"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NudgeDetail(tc.p); got != tc.want {
				t.Errorf("NudgeDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseHookPayloadToolFields(t *testing.T) {
	p, ok := ParseHookPayload(strings.NewReader(
		`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"npm test"},"permission_rule":"Bash(npm *)"}`))
	if !ok || p.ToolName != "Bash" || p.ToolInput.Command != "npm test" || p.PermissionRule != "Bash(npm *)" {
		t.Fatalf("payload = %+v ok=%v", p, ok)
	}
}
```

`toolInput` is a tiny test helper (the field is an anonymous struct):

```go
func toolInput(cmd string) struct {
	Command string `json:"command"`
} {
	return struct {
		Command string `json:"command"`
	}{Command: cmd}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hub -run 'TestNudgeTextDetail|TestNudgeDetail|TestParseHookPayloadToolFields'`
Expected: FAIL — wrong arity on `NudgeText`, `NudgeDetail` undefined,
payload fields missing.

- [ ] **Step 3: Implement**

`internal/hub/hook.go` — extend `HookPayload` (after `NotificationType`):

```go
	// PermissionRequest extras — what the dialog is about, so a nudge
	// can carry the substance and spare the arbiter a peek round-trip.
	ToolName       string `json:"tool_name"`
	PermissionRule string `json:"permission_rule"`
	ToolInput      struct {
		Command string `json:"command"`
	} `json:"tool_input"`
```

`internal/hub/arbiter.go` — replace `NudgeText` and add `NudgeDetail`
(NudgeDetail may live in hook.go beside `hookAction` if the implementer
prefers; keep it in ONE place and say so in the return):

```go
// nudgeDetailMax bounds the trigger detail inside a nudge — the whole
// line is typed into the arbiter's composer, and a runaway tool_input
// command should not become a wall of text there.
const nudgeDetailMax = 160

// NudgeText is the message typed into the arbiter's pane when a session
// needs input. It names the mode so the arbiter doesn't waste a turn on
// an action the answer gate would refuse, and carries the trigger
// detail when the hook event had one ("" for catch-up nudges, which
// have no payload). The detail is sanitized here, not at the call site:
// it is typed into a live claude pane, where a control byte is an
// injection vector and a newline submits early.
func NudgeText(session, mode, detail string) string {
	s := fmt.Sprintf("coop: session %q needs input (mode: %s", session, mode)
	if detail = sanitizeNote(detail); detail != "" {
		if r := []rune(detail); len(r) > nudgeDetailMax {
			detail = string(r[:nudgeDetailMax-1]) + "…"
		}
		s += "; " + detail
	}
	return s + ")"
}

// NudgeDetail renders a hook event's substance for NudgeText: the tool
// and its command for a permission request (falling back to the matched
// rule when the input has no command field), a short word for the
// dialog-shaped notifications. "" for everything else.
func NudgeDetail(p HookPayload) string {
	switch p.Event {
	case "PermissionRequest":
		d := p.ToolName
		switch {
		case p.ToolInput.Command != "":
			d += ": " + p.ToolInput.Command
		case p.PermissionRule != "" && d != "":
			d += ": " + p.PermissionRule
		}
		return d
	case "Notification":
		switch p.NotificationType {
		case "elicitation_dialog":
			return "question"
		case "agent_needs_input":
			return "agent question"
		case "permission_prompt":
			return "permission"
		}
	}
	return ""
}
```

Update the one existing caller, `Apply` (arbiter.go ~205):
`NudgeText(p.Session, ArbiterModeOf(arb), "")`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hub && go build ./...`
Expected: PASS.

---

### Task 2: `HookNudge` + episode cleanup in `ApplyHook` + CLI wiring

**Files:**
- Modify: `internal/hub/hook.go`
- Modify: `cmd/coop/hookcli.go` (~line 54, after `ApplyHook`)
- Test: `internal/hub/hook_test.go`

**Interfaces:**
- Consumes: `NudgeText`/`NudgeDetail` (Task 1), `FindArbiter(panes) (Pane, bool)`
  (arbiter.go:28), `ArbiterModeOf(Pane) string`, markers `ArbiterNudgedMarker`,
  `ArbiterNoteMarker`, `ArbiterSuggestMarker` (tmux.go).
- Produces: `HookNudge(tm Tmux, selfPane string, p HookPayload, now time.Time)`
  (no return — pure best-effort); `ApplyHook` additionally unsets the three
  episode markers on any status change away from `waiting`. Task 5's e2e
  asserts the nudge line format.

- [ ] **Step 1: Write the failing tests**

Add to `internal/hub/hook_test.go`. A shared fixture builder:

```go
// nudgePanes is a socket snapshot for HookNudge tests: our own claude
// pane %1 (session alpha), the arbiter, and a hub session.
func nudgePanes(arbAge time.Duration, now time.Time, selfNudged bool) []Pane {
	return []Pane{
		{Session: "alpha", ID: "%1", ArbiterNudgedMark: selfNudged},
		{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "recommend",
			Created: now.Add(-arbAge)},
		{Session: "roost", ID: "%0", Hub: true},
	}
}

func TestHookNudge(t *testing.T) {
	now := time.Unix(1753900300, 0)
	waiting := HookPayload{Event: "PermissionRequest", SessionID: "s1",
		ToolName: "Bash"}
	waiting.ToolInput.Command = "npm test"

	t.Run("nudges the arbiter once", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 1 {
			t.Fatalf("sent = %v, want one nudge", f.sent)
		}
		want := `coop: session "alpha" needs input (mode: recommend; Bash: npm test)`
		if f.sent[0][0] != want || f.sent[0][1] != "Enter" {
			t.Errorf("keys = %v, want [%q Enter]", f.sent[0], want)
		}
		if f.paneOpts["%1/"+ArbiterNudgedMarker] != "1" {
			t.Error("nudged marker not set")
		}
	})
	t.Run("non-waiting event is a no-op", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%1", HookPayload{Event: "Stop"}, now)
		if len(f.sent) != 0 || len(f.paneOpts) != 0 {
			t.Errorf("no-op expected: sent=%v opts=%v", f.sent, f.paneOpts)
		}
	})
	t.Run("no arbiter: no nudge and NO marker", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)[:1]}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("sent = %v", f.sent)
		}
		// a later arbiter's launch catch-up must still find this pane
		// un-nudged
		if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
			t.Error("marker set with no arbiter running")
		}
	})
	t.Run("young arbiter is skipped, marker unset", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(400*time.Millisecond, now, false)}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("typed at a %v-old arbiter", 400*time.Millisecond)
		}
		if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
			t.Error("marker set on skipped nudge")
		}
	})
	t.Run("already nudged: deduped", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, true)}
		HookNudge(f, "%1", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("second event re-nudged: %v", f.sent)
		}
	})
	t.Run("arbiter's own dialogs don't nudge", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false)}
		HookNudge(f, "%9", waiting, now)
		if len(f.sent) != 0 {
			t.Errorf("arbiter nudged itself: %v", f.sent)
		}
	})
	t.Run("send failure re-arms the marker", func(t *testing.T) {
		f := &fakeTmux{panes: nudgePanes(5*time.Second, now, false),
			sendErr: errors.New("gone")}
		HookNudge(f, "%1", waiting, now)
		if _, ok := f.paneOpts["%1/"+ArbiterNudgedMarker]; ok {
			t.Error("marker left set after failed send — catch-up could never retry")
		}
	})
}

func TestApplyHookClearsEpisodeMarkers(t *testing.T) {
	f := &fakeTmux{}
	seed := func() {
		f.SetPaneOption("%1", ArbiterNudgedMarker, "1")
		f.SetPaneOption("%1", ArbiterNoteMarker, "check the diff")
		f.SetPaneOption("%1", ArbiterSuggestMarker, "2")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// waiting -> busy retires the episode
	must(ApplyHook(f, "%1", HookPayload{Event: "PermissionRequest"}, time.Unix(1, 0)))
	seed()
	must(ApplyHook(f, "%1", HookPayload{Event: "PostToolUse"}, time.Unix(2, 0)))
	for _, m := range []string{ArbiterNudgedMarker, ArbiterNoteMarker, ArbiterSuggestMarker} {
		if _, ok := f.paneOpts["%1/"+m]; ok {
			t.Errorf("%s survived waiting→busy", m)
		}
	}
	// busy -> busy (no transition) leaves a fresh episode's markers alone
	seed()
	must(ApplyHook(f, "%1", HookPayload{Event: "PostToolUse"}, time.Unix(3, 0)))
	if _, ok := f.paneOpts["%1/"+ArbiterNoteMarker]; !ok {
		t.Error("busy→busy cleared markers without a transition")
	}
}
```

(`errors` may need importing in hook_test.go.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hub -run 'TestHookNudge|TestApplyHookClearsEpisodeMarkers'`
Expected: FAIL — `HookNudge` undefined; episode markers survive.

- [ ] **Step 3: Implement**

`internal/hub/hook.go` — in `ApplyHook`, after the status write succeeds
(after the `SetPaneOption(pane, ClaudeStatusMarker, status)` error check,
before the since write):

```go
	// Leaving waiting ends the needs-input episode: retire the nudge
	// dedupe and any note/suggestion the arbiter parked on the row —
	// the cleanup ArbiterNudger.Apply used to do from the hub poll.
	// Best-effort blind unsets (set -pu on an absent option is fine);
	// a miss self-heals when the pane dies.
	if status != "waiting" {
		for _, m := range []string{ArbiterNudgedMarker, ArbiterNoteMarker, ArbiterSuggestMarker} {
			tm.UnsetPaneOption(pane, m)
		}
	}
```

Add `HookNudge` at the bottom of hook.go:

```go
// arbiterReadyAge gates typing at a freshly launched arbiter. Keys
// typed at a claude younger than about half a second land in its
// composer with the trailing Enter absorbed (measured on 2.1.220:
// sends at 0.45s stick, at 0.5s go through); one second is twice that.
// This replaces the deleted @coop_arbiter_seen poll-tick gate.
const arbiterReadyAge = time.Second

// HookNudge tells the arbiter this pane just entered needs-input. It
// runs inside coop hook, so the event itself is the dedupe unit — no
// leader election, no hub required; nudges keep flowing with every TUI
// detached. Entirely best-effort and silent, like the rest of the hook
// path: any miss here is a nudge the operator's own eyes still cover
// via the NEEDS INPUT row.
func HookNudge(tm Tmux, selfPane string, p HookPayload, now time.Time) {
	if status, _, _ := hookAction(p); status != "waiting" {
		return
	}
	panes, err := tm.ListSessions()
	if err != nil {
		return
	}
	arb, ok := FindArbiter(panes)
	// A skipped nudge must not set the marker: the launch catch-up is
	// what retries panes that went waiting before the arbiter was ready,
	// and it only looks at un-nudged panes.
	if !ok || now.Sub(arb.Created) < arbiterReadyAge {
		return
	}
	var self *Pane
	for i := range panes {
		if panes[i].ID == selfPane {
			self = &panes[i]
			break
		}
	}
	if self == nil || self.Arbiter || self.Hub || self.ArbiterNudgedMark {
		return
	}
	tm.SetPaneOption(selfPane, ArbiterNudgedMarker, "1")
	if err := tm.SendKeys(arb.ID,
		NudgeText(self.Session, ArbiterModeOf(arb), NudgeDetail(p)), "Enter"); err != nil {
		// Re-arm so a later arbiter relaunch's catch-up can retry.
		tm.UnsetPaneOption(selfPane, ArbiterNudgedMarker)
	}
}
```

`cmd/coop/hookcli.go` — in `runHookCLI`, after the `ApplyHook` call:

```go
	// The nudge shares the event's process: fires once per dialog, needs
	// no hub attached, and carries the payload's substance.
	hub.HookNudge(tm, pane, p, time.Now())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hub ./cmd/coop && go build ./...`
Expected: PASS.

---

### Task 3: Launch catch-up in the hub poll

**Files:**
- Modify: `internal/hub/arbiter.go` (new `CatchupNudge`, near `FindArbiter`)
- Modify: `internal/tui/model.go` (`createdMsg` ~line 37, Model fields ~75,
  `poll` ~154-196, `pollMsg` struct ~27, `pollMsg` handler ~507,
  `createdMsg` handler ~618, `a`-key arbiter launch ~953,
  `arbiterCreateCmd` ~1128)
- Test: `internal/hub/arbiter_test.go`, `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `NudgeText(session, mode, "")`, `FindArbiter`, `ArbiterModeOf`,
  `arbiterReadyAge` (Task 2).
- Produces: `CatchupNudge(tm Tmux, panes []Pane, arb Pane)`; Model field
  `arbiterCatchup bool`; `pollMsg.caughtUp bool`; `createdMsg.arbiter bool`.
  Task 4 deletes `nudge.Apply` knowing this replaced it.

- [ ] **Step 1: Write the failing hub test**

Add to `internal/hub/arbiter_test.go`:

```go
func TestCatchupNudge(t *testing.T) {
	arb := Pane{Session: "arbiter", ID: "%9", Arbiter: true, ArbiterMode: "full"}
	panes := []Pane{
		// hook-published waiting, un-nudged: gets the catch-up
		{Session: "alpha", ID: "%1", Claude: &ClaudeState{Status: "waiting"}},
		// already nudged: skipped
		{Session: "beta", ID: "%2", Claude: &ClaudeState{Status: "waiting"}, ArbiterNudgedMark: true},
		// busy: skipped
		{Session: "gamma", ID: "%3", Claude: &ClaudeState{Status: "busy"}},
		// title-tier needs-input with no hook state: skipped — nothing
		// would ever clear a marker set here
		{Session: "delta", ID: "%4", Title: "🔔 pick one"},
		arb,
		{Session: "roost", ID: "%0", Hub: true},
	}
	f := &fakeTmux{}
	CatchupNudge(f, panes, arb)
	if len(f.sent) != 1 {
		t.Fatalf("sent = %v, want exactly alpha's nudge", f.sent)
	}
	if want := `coop: session "alpha" needs input (mode: full)`; f.sent[0][0] != want {
		t.Errorf("nudge = %q, want %q", f.sent[0][0], want)
	}
	if f.paneOpts["%1/"+ArbiterNudgedMarker] != "1" {
		t.Error("alpha's marker not set")
	}
	if _, ok := f.paneOpts["%4/"+ArbiterNudgedMarker]; ok {
		t.Error("title-tier pane acquired a marker")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/hub -run TestCatchupNudge`
Expected: FAIL — `CatchupNudge` undefined.

- [ ] **Step 3: Implement `CatchupNudge`**

`internal/hub/arbiter.go`:

```go
// CatchupNudge nudges every pane already waiting when the arbiter
// launched — their events fired before it existed, so the hook path
// can never tell it about them. Run once by the hub that launched the
// arbiter (no election: exactly one hub launches). It gates on the
// hook-published status, not the derived one: a title-tier pane must
// never acquire a nudged marker, because no hook will ever clear it.
// Best-effort throughout; a failed send re-arms the marker so a future
// relaunch's catch-up can retry.
func CatchupNudge(tm Tmux, panes []Pane, arb Pane) {
	for i := range panes {
		p := &panes[i]
		if p.Arbiter || p.Hub || p.ArbiterNudgedMark ||
			p.Claude == nil || p.Claude.Status != "waiting" {
			continue
		}
		tm.SetPaneOption(p.ID, ArbiterNudgedMarker, "1")
		if err := tm.SendKeys(arb.ID,
			NudgeText(p.Session, ArbiterModeOf(arb), ""), "Enter"); err != nil {
			tm.UnsetPaneOption(p.ID, ArbiterNudgedMarker)
		}
	}
}
```

Run: `go test ./internal/hub -run TestCatchupNudge` — PASS.

- [ ] **Step 4: Wire the tui flag (surgical edits to model.go)**

- `createdMsg` gains a field: `type createdMsg struct { err error; arbiter bool }`
  — `arbiterCreateCmd`'s closure returns `createdMsg{err: …, arbiter: true}`;
  `createCmd`'s stays `createdMsg{err: …}`.
- Model gains `arbiterCatchup bool` (next to the other arbiter fields ~line
  75), with the comment: `// a just-launched arbiter owes already-waiting panes a one-shot catch-up nudge`.
- The `a`-key branch that returns `m.arbiterCreateCmd()` (~line 953) sets
  `m.arbiterCatchup = true` first.
- The `createdMsg` handler (~line 618): if `msg.arbiter && msg.err != nil`,
  set `m.arbiterCatchup = false` (launch failed; nothing to catch up).
- `pollMsg` struct gains `caughtUp bool`.
- In `poll()` (~line 154): capture `catchup := m.arbiterCatchup` with the
  other by-value captures. Inside the closure, where `nudge.Apply(panes, hubs)`
  runs today, add after it (Task 4 deletes the Apply line, not this):

```go
		caughtUp := false
		if catchup {
			// The launch catch-up runs here in the poll closure — same
			// place the old nudger lived — because the pollMsg handler's
			// single command is already spoken for (retarget). Gated on
			// the same readiness age as HookNudge; until the arbiter is
			// old enough the flag just rides to the next tick.
			if arb, ok := hub.FindArbiter(panes); ok && time.Since(arb.Created) >= time.Second {
				hub.CatchupNudge(tm, panes, arb)
				caughtUp = true
			}
		}
```

  and return it: `pollMsg{…, caughtUp: caughtUp, …}`.
- `pollMsg` handler (~line 507): `if msg.caughtUp { m.arbiterCatchup = false }`
  next to the other field updates.

- [ ] **Step 5: Write the tui test and verify the package**

Add to `internal/tui/model_test.go` (mirror the file's existing
`pollOnce`/`drive` helpers — read a neighboring test first):

```go
// Launching the arbiter arms a one-shot catch-up: the first poll that
// sees an old-enough arbiter nudges already-waiting hook panes, then
// the flag clears.
func TestArbiterLaunchCatchup(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "alpha", ID: "%1", Title: "✳ Claude Code",
			Claude: &hub.ClaudeState{Status: "waiting"}},
		{Session: "arbiter", ID: "%9", Arbiter: true,
			Created: time.Now().Add(-5 * time.Second)},
	}}
	m := pollOnce(t, f)
	m.arbiterCatchup = true
	m = drive(t, m, m.poll())
	if m.arbiterCatchup {
		t.Error("flag should clear once the catch-up ran")
	}
	found := false
	for _, keys := range f.sent {
		if strings.Contains(keys[0], `session "alpha" needs input`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no catch-up nudge in sent keys: %v", f.sent)
	}
}

// A young arbiter defers the catch-up to a later tick instead of
// typing into the key-swallow window.
func TestArbiterLaunchCatchupWaitsForAge(t *testing.T) {
	f := &fakeTmux{panes: []hub.Pane{
		{Session: "alpha", ID: "%1", Title: "✳ Claude Code",
			Claude: &hub.ClaudeState{Status: "waiting"}},
		{Session: "arbiter", ID: "%9", Arbiter: true, Created: time.Now()},
	}}
	m := pollOnce(t, f)
	m.arbiterCatchup = true
	m = drive(t, m, m.poll())
	if !m.arbiterCatchup {
		t.Error("flag consumed before the arbiter was old enough")
	}
	if len(f.sent) != 0 {
		t.Errorf("typed at a fresh arbiter: %v", f.sent)
	}
}
```

Run: `go test ./internal/tui ./internal/hub`
Expected: PASS.

---

### Task 4: Delete the poll-driven nudger

**Files:**
- Modify: `internal/hub/arbiter.go` (delete `ArbiterNudger`,
  `NewArbiterNudger`, `leads`, `Apply` — the block at ~125-221)
- Modify: `internal/hub/tmux.go` (delete `ArbiterSeenMarker` const, the
  `ArbiterSeen` field, its `paneFormat` entry and `parsePanes` index —
  **23 fields become 22 and every index after 14 shifts down one**)
- Modify: `internal/hub/tmux_test.go` (all fixtures: remove the seen field
  — the 15th field — from every 23-field line, raw and `\037` forms)
- Modify: `internal/hub/arbiter_test.go` (delete `ArbiterNudger`/`leads`/
  `Apply` tests; keep `TestCatchupNudge`, `TestNudgeTextDetail`)
- Modify: `internal/tui/model.go` (delete the `nudge` field, its `New`
  initializer, the `nudge := m.nudge` capture and `nudge.Apply(panes, hubs)`
  line in `poll` — the `hubs` slice itself STAYS: `pollMsg.hubs` feeds
  `m.hubNames`, which `sessionNames` (~line 1082) still consumes)
- Modify: `internal/tui/model_test.go` (fix anything referencing the
  deleted symbols or constructing `ArbiterSeen`)
- Test: full suite

**Interfaces:**
- Consumes: Tasks 2–3 (the replacement paths must already exist).
- Produces: deleted symbols: `ArbiterNudger`, `NewArbiterNudger`, `leads`,
  `Apply`, `ArbiterSeenMarker`, `Pane.ArbiterSeen`.

- [ ] **Step 1: Delete hub-side**

In `arbiter.go` remove the whole nudger block (struct through `Apply`,
including the key-swallow comment — its measurement now lives on
`arbiterReadyAge`). In `tmux.go`: remove the `ArbiterSeenMarker` const and
the `ArbiterSeen` field; remove `#{@coop_arbiter_seen}` from `paneFormat`;
in `parsePanes` change the length check to `len(f) != 22` and renumber —
after `ArbiterMode: f[13]` the assignments become `ArbiterNudgedMark: f[14] == "1"`,
`ArbiterNote: f[15]`, `ArbiterSuggest: f[16]`, `ArbiterLast: f[17]`, and the
Claude block reads `f[18]`–`f[21]`.

- [ ] **Step 2: Fix the tests that notice**

`tmux_test.go`: every fixture line loses its seen field (they were built in
the status-tier change; the seen field is the 15th — count carefully in the
`\037` forms). `arbiter_test.go`: delete the nudger tests wholesale.
`model.go`/`model_test.go`: grep-driven:

```bash
grep -rn "ArbiterNudger\|ArbiterSeen\|nudge" --include="*.go" internal/ cmd/ | grep -v "Nudge\b\|NudgeText\|NudgeDetail\|HookNudge\|CatchupNudge\|nudgePanes\|ArbiterNudged"
```

Expected after cleanup: no hits.

- [ ] **Step 3: Full suite**

Run: `go test ./...`
Expected: PASS.

---

### Task 5: e2e nudge scenario + CLAUDE.md

**Files:**
- Modify: `scripts/e2e-smoke.sh` (fake arbiter + nudge assertion)
- Modify: `CLAUDE.md` (arbiter section, ~the `ArbiterNudger.Apply` paragraph)
- Test: `go test ./...` + `scripts/e2e-smoke.sh`

**Interfaces:**
- Consumes: the stub already pipes a PermissionRequest payload through the
  real `coop hook`; `HookNudge` (Task 2) fires off that same invocation.

- [ ] **Step 1: Add the fake arbiter to the e2e**

In `scripts/e2e-smoke.sh`, right after the stub session is created and
before the hub launches, add a fake arbiter whose pane echoes what is typed
at it (a `sleep` pane wouldn't reliably render sent keys):

```bash
# Fake arbiter: a read-echo loop, so the nudge coop hook types at it
# lands as a visible GOT: line we can assert on. Created before the
# stub's hook fires (the stub sleeps first) so the 1s readiness age
# gate has passed by then.
mkdir -p "$TMPD/arb"
tmux -L "$SOCKET" new-session -d -s arbiter -c "$TMPD/arb" \
  'while IFS= read -r l; do echo "GOT:$l"; done'
tmux -L "$SOCKET" set -t arbiter: @coop_arbiter 1
```

Change the stub's initial `sleep 1` to `sleep 3` (both to outlast the
readiness gate and unchanged in purpose — the monitor-bell window), and
enrich its payload so the nudge detail is assertable:

```bash
  "sleep 3; printf 'Do you want to proceed?\n❯ 1. Yes\n  2. No\n\a'; printf '{\"hook_event_name\":\"PermissionRequest\",\"session_id\":\"stub\",\"cwd\":\"$TMPD/stub\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"rm -rf build/\"}}' | /tmp/coop-e2e hook; sleep 300"
```

After the existing `ok: discovery, status, live preview` block, assert the
nudge arrived:

```bash
# The stub's hook invocation must also have nudged the arbiter, with
# the payload's tool detail in the line.
wait_for 'GOT:coop: session "stub" needs input (mode: recommend; Bash: rm -rf build/)' arbiter
echo "ok: hook nudged the arbiter with tool detail"
```

Check `wait_for`'s pattern handling first — it greps; the parens/quotes
need to survive, so use a distinctive substring if the full line is
grep-hostile: `wait_for 'mode: recommend; Bash: rm -rf build/' arbiter`.

One knock-on to verify: the e2e's kill/answer scenarios iterate sessions —
the new `arbiter` session sorts last in the TUI (SortPanes pins it) and
`tab` skips it, so the existing `j`-navigation counts may shift. Run the
script; if a later step targets the wrong row, adjust that step's
key-count with a comment, not the arbiter's position.

- [ ] **Step 2: Update CLAUDE.md's arbiter section**

Replace the `ArbiterNudger.Apply` paragraph: nudges now originate in
`coop hook` (`HookNudge`) at the moment a waiting-type event fires — one
process, one event, one nudge, so no `leads` election and no
`@coop_arbiter_seen`; they work with no hub attached; the payload's tool
detail rides in the text. The readiness gate is `arbiterReadyAge` (1s,
twice the measured 0.5s key-swallow threshold). The one hub-side piece is
the launch catch-up (`CatchupNudge`, run once from the poll closure by the
hub that launched the arbiter, gated on hook-published waiting). Episode
cleanup (nudged/note/suggest) happens in `ApplyHook` when status leaves
waiting. Note the accepted gaps: a dialog firing inside the 1s window
after catch-up is nudged by neither path, and non-hook panes never nudge.
Also drop the `@coop_arbiter_seen` entry from the user-options list in the
"State rides on tmux options" paragraph. Match the file's voice.

- [ ] **Step 3: Full gates**

Run: `go test ./...` then `scripts/e2e-smoke.sh`
Expected: PASS, including the new `ok: hook nudged the arbiter…` line.

Live verification with a real claude+arbiter is deliberately not scripted
here (it spends real API turns on the arbiter); the e2e covers the
mechanics with the real `coop hook` binary. Note this in the run report.

---

## Self-Review (completed)

- **Spec coverage:** hook-side nudge with all four skip-gates → Task 2;
  enriched text + sanitization → Task 1; marker lifecycle (set on nudge,
  cleared on leaving waiting, note/suggest retired with it — the Apply-tail
  behavior the spec implies) → Task 2; launch catch-up gated on
  hook-published waiting → Task 3; deletions incl. seen-marker and format
  renumbering → Task 4; "no hub attached" property → inherent in Task 2,
  asserted by the e2e (no hub involvement in the nudge path); docs → Task 5.
  Spec's open item 1 (co-firing of PermissionRequest + Notification for one
  dialog) is handled by the marker dedupe regardless of the answer, with
  PermissionRequest's richer payload winning when it fires first — noted in
  `HookNudge`'s dedupe test.
- **Placeholder scan:** none.
- **Type consistency:** `NudgeText(session, mode, detail string)` used in
  Tasks 1/2/3/5; `HookNudge(tm, selfPane, p, now)` in 2/5;
  `CatchupNudge(tm, panes, arb)` in 3; `arbiterReadyAge` defined in 2,
  referenced in 3's comment and 5's docs; `createdMsg.arbiter` and
  `pollMsg.caughtUp` only in Task 3.
