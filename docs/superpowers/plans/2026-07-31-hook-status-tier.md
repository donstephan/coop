# Hook-Based Status Tier Implementation Plan

> **For agentic workers:** This plan is executed with the operator's `sdd`
> skill (fresh implementer subagent per task, one whole-branch review at the
> end). **Never run git commands** — no commits, no add, nothing; the
> operator handles git. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace coop's undocumented pid-file status tier with hooks coop
injects into every session it launches, publishing status as tmux pane user
options.

**Architecture:** A `coop hook` subcommand (registered for 8 Claude Code hook
events via a `--settings` file appended to `claudeCmd` at launch) writes
`@coop_claude_status` / `@coop_status_since` / `@coop_session_id` /
`@coop_claude_cwd` onto its own pane using `$TMUX_PANE`. The poll reads them
back through the existing `list-panes` format string. The pid-file tier
(`ClaudeSessions`) and the screen-capture fallback in `DeriveStatuses` are
deleted; the pane-title heuristic remains the only fallback.

**Tech Stack:** Go, tmux CLI (`-L` socket), Claude Code hooks
(`hook_event_name` JSON on stdin), Bubble Tea TUI.

**Spec:** `docs/superpowers/specs/2026-07-30-hook-status-tier-design.md`

## Global Constraints

- **No git commands, ever** — the operator handles all git; sdd applies
  results as uncommitted edits.
- **No real paths or names in tests/fixtures**: never `/home/don/…`; use
  `/home/user/alpha`, session names `alpha`, `beta`, `sprocket-v2`;
  anything written to disk goes under `t.TempDir()`.
- **Never split `-F` output with `strings.Split(line, "\x1f")` directly** —
  always `splitFields` (tmux ≤ 3.4 vis(3)-escapes the separator to `\037`).
- **tui single-command invariant**: `Update` returns at most one command per
  message; all tmux I/O inside `tea.Cmd` closures capturing by value.
- **Best-effort writes**: tmux writes that can fail get a comment saying so;
  a failure leaves stale state that the next event/poll corrects.
- Status vocabulary is Claude's own: `busy` | `waiting` | `idle` — never
  invent new values.
- Hook process discipline: always exit 0, nothing on stdout, nothing on
  stderr.
- After each task: run the tests named in that task. Full `go test ./...`
  only in the final task.
- Note: `internal/tui/model.go` and `model_test.go` carry uncommitted
  operator edits — modify surgically, never revert unrelated hunks.

---

### Task 1: Spike — hooks via `--settings` fire without review

**Files:**
- Create: `<scratchpad>/spike-hooks/settings.json`, `<scratchpad>/spike-hooks/log`
  (scratchpad only — nothing in the repo)

**Interfaces:**
- Produces: a go/no-go decision. If hooks do NOT fire, **STOP the plan** and
  report — the spec's fallback (user-level settings with a one-time ask)
  needs a design amendment first.

- [ ] **Step 1: Write the spike settings file**

Write `<scratchpad>/spike-hooks/settings.json` (substitute the real
scratchpad path for `<SP>` throughout):

```json
{
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "cat >> <SP>/spike-hooks/log", "timeout": 5}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "cat >> <SP>/spike-hooks/log", "timeout": 5}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "cat >> <SP>/spike-hooks/log", "timeout": 5}]}]
  }
}
```

`cat` copies each event's stdin JSON into the log, which both proves the
hook fired and shows the real payload fields.

- [ ] **Step 2: Run a non-interactive claude with it**

```bash
cd <SP>/spike-hooks && claude -p 'reply with just ok' --settings <SP>/spike-hooks/settings.json > out.txt 2> err.txt; echo "exit=$?"
```

- [ ] **Step 3: Judge the result**

```bash
cat <SP>/spike-hooks/log; cat <SP>/spike-hooks/err.txt
```

PASS: the log holds at least `SessionStart`, `UserPromptSubmit`, and `Stop`
JSON lines, each containing `session_id`, `cwd`, `hook_event_name`, and
`err.txt` shows no hook-approval gate. Record the observed payloads in the
task report (they validate Task 3's field names). FAIL (empty log, or a
review/approval message): **stop the plan, report the evidence.**

---

### Task 2: Read side — parse hook-published options into `Pane.Claude`

**Files:**
- Modify: `internal/hub/tmux.go` (constants ~line 124, `paneFormat` ~148,
  `parsePanes` ~178)
- Modify: `internal/hub/claudestate.go:153-163` (`AttachClaudeState` guard)
- Test: `internal/hub/tmux_test.go`

**Interfaces:**
- Produces: constants `ClaudeStatusMarker = "@coop_claude_status"`,
  `ClaudeSinceMarker = "@coop_status_since"`,
  `ClaudeSessionMarker = "@coop_session_id"`,
  `ClaudeCWDMarker = "@coop_claude_cwd"`; `parsePanes` emits 23 fields and
  fills `Pane.Claude *ClaudeState` (`Status`, `StatusSince`, `SessionID`,
  `CWD`) whenever the status field is non-empty. Tasks 3–7 rely on these
  exact names.

- [ ] **Step 1: Write the failing test**

Add to `internal/hub/tmux_test.go`:

```go
func TestParsePanesClaudeOptions(t *testing.T) {
	base := []string{
		"alpha", "%1", "42", "✳ Claude Code", "0", "claude",
		"1753900000", "/home/user/alpha",
		"", "", "", "", "", "", "", "", "", "", "",
	}
	published := strings.Join(append(append([]string{}, base...),
		"waiting", "1753900100", "sess-1", "/home/user/alpha"), "\x1f")
	unpublished := strings.Join(append(append([]string{}, base...),
		"", "", "", ""), "\x1f")

	panes := parsePanes(published + "\n" + unpublished)
	if len(panes) != 2 {
		t.Fatalf("panes = %d, want 2", len(panes))
	}
	c := panes[0].Claude
	if c == nil {
		t.Fatal("published pane: Claude = nil")
	}
	if c.Status != "waiting" || c.SessionID != "sess-1" || c.CWD != "/home/user/alpha" {
		t.Errorf("Claude = %+v", c)
	}
	if got := c.StatusSince.Unix(); got != 1753900100 {
		t.Errorf("StatusSince = %d, want 1753900100", got)
	}
	if panes[1].Claude != nil {
		t.Errorf("unpublished pane: Claude = %+v, want nil", panes[1].Claude)
	}
}

func TestParsePanesClaudeOptionsEscaped(t *testing.T) {
	fields := []string{
		"alpha", "%1", "42", "t", "0", "claude", "1753900000", "/home/user/alpha",
		"", "", "", "", "", "", "", "", "", "", "",
		"busy", "1753900100", "sess-1", "/home/user/alpha",
	}
	panes := parsePanes(strings.Join(fields, `\037`)) // tmux ≤ 3.4 vis(3) form
	if len(panes) != 1 || panes[0].Claude == nil || panes[0].Claude.Status != "busy" {
		t.Fatalf("escaped form not parsed: %+v", panes)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hub -run TestParsePanesClaude`
Expected: FAIL (panes drop the 23-field lines — `len(f) != 19`).

- [ ] **Step 3: Implement**

In `internal/hub/tmux.go`, extend the constants block (after
`ArbiterLastMarker`):

```go
	// Hook-published Claude state (see hook.go): the injected coop
	// hooks write these onto their own pane; the poll reads them back
	// through paneFormat. Absent on sessions launched outside coop,
	// which fall back to the title heuristics.
	ClaudeStatusMarker  = "@coop_claude_status" // "busy" | "waiting" | "idle"
	ClaudeSinceMarker   = "@coop_status_since"  // unix seconds it entered Status
	ClaudeSessionMarker = "@coop_session_id"    // names the transcript file
	ClaudeCWDMarker     = "@coop_claude_cwd"    // claude's cwd — the project slug
```

Append the four to `paneFormat` (keep the existing 19 in order):

```go
const paneFormat = "…existing…\x1f#{" + ClaudeStatusMarker + "}\x1f#{" + ClaudeSinceMarker + "}\x1f#{" + ClaudeSessionMarker + "}\x1f#{" + ClaudeCWDMarker + "}"
```

In `parsePanes`, change the length check to `len(f) != 23` and build the
state after the existing struct literal fields:

```go
		p := Pane{ /* existing fields f[0]..f[18] unchanged */ }
		if f[19] != "" {
			p.Claude = &ClaudeState{Status: f[19], StatusSince: unixTime(f[20]),
				SessionID: f[21], CWD: f[22]}
		}
		panes = append(panes, p)
```

In `AttachClaudeState` (claudestate.go), skip panes the options already
covered — the hook tier outranks the pid files during the transition (the
whole function is deleted in Task 7):

```go
	for i := range panes {
		if panes[i].PID <= 0 || panes[i].Claude != nil {
			continue
		}
		panes[i].Claude = lookup(panes[i].PID)
	}
```

- [ ] **Step 4: Fix existing fixtures and verify package passes**

Every existing `parsePanes` fixture in `tmux_test.go` uses 19 fields —
append four empty fields (`"\x1f\x1f\x1f\x1f"` raw, `\037\037\037\037`
escaped) to each. Then:

Run: `go test ./internal/hub`
Expected: PASS.

---

### Task 3: `Tmux.PaneOption` + the hook writer (`internal/hub/hook.go`)

**Files:**
- Modify: `internal/hub/tmux.go` (interface ~line 33, `ExecTmux` method
  near `SetPaneOption` ~365)
- Modify: `internal/hub/fake_tmux_test.go`
- Modify: `internal/tui/model_test.go` (its `fakeTmux` must also gain
  `PaneOption` to keep implementing `hub.Tmux` — add next to its
  `SetPaneOption` at ~line 127)
- Create: `internal/hub/hook.go`
- Test: `internal/hub/hook_test.go` (new)

**Interfaces:**
- Consumes: Task 2's marker constants.
- Produces:
  - `Tmux.PaneOption(pane, name string) (string, error)` — read one pane
    user option, `""` when unset.
  - `type HookPayload struct { Event, SessionID, CWD, Source, NotificationType string }`
  - `ParseHookPayload(r io.Reader) (HookPayload, bool)`
  - `ApplyHook(tm Tmux, pane string, p HookPayload, now time.Time) error`
  Task 4 calls exactly these.

- [ ] **Step 1: Write the failing tests**

Create `internal/hub/hook_test.go`:

```go
package hub

import (
	"strings"
	"testing"
	"time"
)

func TestParseHookPayload(t *testing.T) {
	p, ok := ParseHookPayload(strings.NewReader(
		`{"hook_event_name":"Notification","session_id":"s1","cwd":"/home/user/alpha","notification_type":"idle_prompt"}`))
	if !ok || p.Event != "Notification" || p.SessionID != "s1" ||
		p.CWD != "/home/user/alpha" || p.NotificationType != "idle_prompt" {
		t.Fatalf("payload = %+v ok=%v", p, ok)
	}
	if _, ok := ParseHookPayload(strings.NewReader("not json")); ok {
		t.Error("garbage parsed as payload")
	}
	if _, ok := ParseHookPayload(strings.NewReader(`{"session_id":"s1"}`)); ok {
		t.Error("payload without hook_event_name accepted")
	}
}

func TestApplyHookStatusMapping(t *testing.T) {
	now := time.Unix(1753900200, 0)
	cases := []struct {
		name    string
		payload HookPayload
		status  string // "" = no status write expected
	}{
		{"prompt", HookPayload{Event: "UserPromptSubmit"}, "busy"},
		{"post tool", HookPayload{Event: "PostToolUse"}, "busy"},
		{"denied", HookPayload{Event: "PermissionDenied"}, "busy"},
		{"permission", HookPayload{Event: "PermissionRequest"}, "waiting"},
		{"notify permission", HookPayload{Event: "Notification", NotificationType: "permission_prompt"}, "waiting"},
		{"notify question", HookPayload{Event: "Notification", NotificationType: "elicitation_dialog"}, "waiting"},
		{"notify agent", HookPayload{Event: "Notification", NotificationType: "agent_needs_input"}, "waiting"},
		{"notify idle", HookPayload{Event: "Notification", NotificationType: "idle_prompt"}, "idle"},
		{"notify other", HookPayload{Event: "Notification", NotificationType: "auth_success"}, ""},
		{"stop", HookPayload{Event: "Stop"}, "idle"},
		{"start fresh", HookPayload{Event: "SessionStart", Source: "startup"}, "idle"},
		{"start resume", HookPayload{Event: "SessionStart", Source: "resume"}, "idle"},
		{"start clear", HookPayload{Event: "SessionStart", Source: "clear"}, "idle"},
		{"start compact", HookPayload{Event: "SessionStart", Source: "compact"}, ""},
		{"start fork", HookPayload{Event: "SessionStart", Source: "fork"}, ""},
		{"unknown event", HookPayload{Event: "SubagentStop"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeTmux{}
			if err := ApplyHook(f, "%1", tc.payload, now); err != nil {
				t.Fatal(err)
			}
			if got := f.paneOpts["%1/"+ClaudeStatusMarker]; got != tc.status {
				t.Errorf("status = %q, want %q", got, tc.status)
			}
			wantSince := ""
			if tc.status != "" {
				wantSince = "1753900200"
			}
			if got := f.paneOpts["%1/"+ClaudeSinceMarker]; got != wantSince {
				t.Errorf("since = %q, want %q", got, wantSince)
			}
		})
	}
}

func TestApplyHookSessionStartIdentity(t *testing.T) {
	f := &fakeTmux{}
	// compact: identity still refreshes, status untouched
	err := ApplyHook(f, "%1", HookPayload{Event: "SessionStart", Source: "compact",
		SessionID: "s2", CWD: "/home/user/alpha"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if f.paneOpts["%1/"+ClaudeSessionMarker] != "s2" ||
		f.paneOpts["%1/"+ClaudeCWDMarker] != "/home/user/alpha" {
		t.Errorf("identity not written: %v", f.paneOpts)
	}
	if _, ok := f.paneOpts["%1/"+ClaudeStatusMarker]; ok {
		t.Error("compact wrote a status")
	}
}

func TestApplyHookSinceOnlyOnChange(t *testing.T) {
	f := &fakeTmux{}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(ApplyHook(f, "%1", HookPayload{Event: "UserPromptSubmit"}, time.Unix(100, 0)))
	must(ApplyHook(f, "%1", HookPayload{Event: "PostToolUse"}, time.Unix(200, 0)))
	if got := f.paneOpts["%1/"+ClaudeSinceMarker]; got != "100" {
		t.Errorf("busy→busy reset since to %q, want 100", got)
	}
	must(ApplyHook(f, "%1", HookPayload{Event: "Stop"}, time.Unix(300, 0)))
	if got := f.paneOpts["%1/"+ClaudeSinceMarker]; got != "300" {
		t.Errorf("busy→idle since = %q, want 300", got)
	}
}

func TestApplyHookSessionEndClears(t *testing.T) {
	f := &fakeTmux{}
	if err := ApplyHook(f, "%1", HookPayload{Event: "SessionStart", Source: "startup",
		SessionID: "s1", CWD: "/home/user/alpha"}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyHook(f, "%1", HookPayload{Event: "SessionEnd"}, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if len(f.paneOpts) != 0 {
		t.Errorf("options left after SessionEnd: %v", f.paneOpts)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hub -run 'TestParseHookPayload|TestApplyHook'`
Expected: FAIL — `ParseHookPayload`, `ApplyHook` undefined.

- [ ] **Step 3: Implement**

`internal/hub/tmux.go` — add to the `Tmux` interface, next to
`SetPaneOption`:

```go
	PaneOption(pane, name string) (string, error)
```

and the `ExecTmux` implementation next to `SetPaneOption`:

```go
// PaneOption reads one pane user option; -q makes an unset option an
// empty string rather than an error.
func (t *ExecTmux) PaneOption(pane, name string) (string, error) {
	out, err := t.run("show-options", "-pqv", "-t", pane, name)
	return strings.TrimSpace(out), err
}
```

`internal/hub/fake_tmux_test.go` — next to `SetPaneOption`:

```go
func (f *fakeTmux) PaneOption(pane, name string) (string, error) {
	return f.paneOpts[pane+"/"+name], f.err
}
```

`internal/tui/model_test.go` — the tui `fakeTmux` (type at ~line 21) gains
the same method next to its `SetPaneOption` (~line 127), matching that
fake's existing error-field conventions; if it has no option storage,
return `("", nil)` (its error field permitting).

Create `internal/hub/hook.go`:

```go
package hub

import (
	"encoding/json"
	"io"
	"strconv"
	"time"
)

// HookPayload is the subset of Claude Code's hook stdin JSON that coop
// reads (documented contract: session_id, cwd, hook_event_name in every
// event; source on SessionStart; notification_type on Notification).
type HookPayload struct {
	Event            string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	CWD              string `json:"cwd"`
	Source           string `json:"source"`
	NotificationType string `json:"notification_type"`
}

// ParseHookPayload decodes one hook invocation's stdin. false for
// anything unusable — the caller no-ops rather than guessing.
func ParseHookPayload(r io.Reader) (HookPayload, bool) {
	var p HookPayload
	if json.NewDecoder(r).Decode(&p) != nil || p.Event == "" {
		return HookPayload{}, false
	}
	return p, true
}

// hookAction maps an event onto the pane writes it implies. status ""
// means no status change; identity means refresh session_id/cwd; clear
// means the session ended and every option goes.
func hookAction(p HookPayload) (status string, identity, clear bool) {
	switch p.Event {
	case "SessionStart":
		identity = true
		// compact and fork fire mid-turn — refreshing identity is
		// right, flipping a busy row idle is not.
		switch p.Source {
		case "startup", "resume", "clear":
			status = "idle"
		}
	case "UserPromptSubmit", "PostToolUse", "PermissionDenied":
		status = "busy"
	case "PermissionRequest":
		status = "waiting"
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt", "elicitation_dialog", "agent_needs_input":
			status = "waiting"
		case "idle_prompt":
			status = "idle"
		}
	case "Stop":
		status = "idle"
	case "SessionEnd":
		clear = true
	}
	return
}

// claudeMarkers is every option ApplyHook manages, for SessionEnd.
var claudeMarkers = []string{
	ClaudeStatusMarker, ClaudeSinceMarker, ClaudeSessionMarker, ClaudeCWDMarker,
}

// ApplyHook writes one hook event's state onto the pane. The since
// option only moves when the status actually changes, so a busy→busy
// PostToolUse doesn't reset "working 5m" to zero.
func ApplyHook(tm Tmux, pane string, p HookPayload, now time.Time) error {
	status, identity, clear := hookAction(p)
	if clear {
		for _, m := range claudeMarkers {
			if err := tm.UnsetPaneOption(pane, m); err != nil {
				return err
			}
		}
		return nil
	}
	if identity {
		if err := tm.SetPaneOption(pane, ClaudeSessionMarker, p.SessionID); err != nil {
			return err
		}
		if err := tm.SetPaneOption(pane, ClaudeCWDMarker, p.CWD); err != nil {
			return err
		}
	}
	if status == "" {
		return nil
	}
	if cur, err := tm.PaneOption(pane, ClaudeStatusMarker); err == nil && cur == status {
		return nil
	}
	if err := tm.SetPaneOption(pane, ClaudeStatusMarker, status); err != nil {
		return err
	}
	return tm.SetPaneOption(pane, ClaudeSinceMarker,
		strconv.FormatInt(now.Unix(), 10))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hub -run 'TestParseHookPayload|TestApplyHook|TestFakeImplementsTmux' && go build ./...`
Expected: PASS, clean build (proves both fakes still satisfy `Tmux`).

---

### Task 4: `coop hook` CLI dispatch

**Files:**
- Create: `cmd/coop/hookcli.go`
- Modify: `cmd/coop/main.go:183-187` (dispatch before `isArbiterCmd`)
- Test: `cmd/coop/hookcli_test.go` (new)

**Interfaces:**
- Consumes: `hub.ParseHookPayload`, `hub.ApplyHook`, `hub.ExecTmux`,
  `cliSocket()` (already in `cmd/coop/arbitercli.go`).
- Produces: `runHookCLI(stdin io.Reader, getenv func(string) string) int` —
  always returns 0. Task 6's settings file invokes `coop hook`.

- [ ] **Step 1: Write the failing test**

Create `cmd/coop/hookcli_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

// Outside a tmux pane (or with garbage stdin) the hook is a silent
// no-op that still exits 0 — a hook must never disturb its session.
func TestRunHookCLINoOps(t *testing.T) {
	env := func(map[string]string) func(string) string {
		return func(string) string { return "" }
	}
	cases := []struct {
		name  string
		stdin string
		vars  map[string]string
	}{
		{"no env", `{"hook_event_name":"Stop"}`, nil},
		{"bad stdin", "not json", map[string]string{
			"TMUX_PANE": "%1", "TMUX": "/tmp/tmux-1000/coop,1,0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := env(tc.vars)
			if tc.vars != nil {
				getenv = func(k string) string { return tc.vars[k] }
			}
			if code := runHookCLI(strings.NewReader(tc.stdin), getenv); code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
		})
	}
}

func TestIsHookCmd(t *testing.T) {
	if !isHookCmd([]string{"hook"}) || isHookCmd([]string{"peek"}) || isHookCmd(nil) {
		t.Error("isHookCmd misdetects")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/coop -run 'TestRunHookCLI|TestIsHookCmd'`
Expected: FAIL — `runHookCLI`, `isHookCmd` undefined.

- [ ] **Step 3: Implement**

Create `cmd/coop/hookcli.go`:

```go
package main

import (
	"io"
	"time"

	"coop/internal/hub"
)

// isHookCmd reports whether argv selects the hook subcommand — the
// status publisher Claude Code invokes (via the injected settings file)
// on every hook event.
func isHookCmd(args []string) bool {
	return len(args) > 0 && args[0] == "hook"
}

// runHookCLI publishes one hook event onto this pane's user options.
// It always returns 0 and prints nothing: hook stdout is parsed by
// Claude Code as JSON and stderr lands in the session's transcript, so
// every failure here is a silent no-op — the pane just stays on the
// title fallback until the next event.
func runHookCLI(stdin io.Reader, getenv func(string) string) int {
	pane := getenv("TMUX_PANE")
	if pane == "" || getenv("TMUX") == "" {
		return 0 // not in a tmux pane coop could mark
	}
	p, ok := hub.ParseHookPayload(stdin)
	if !ok {
		return 0
	}
	tm := &hub.ExecTmux{Socket: cliSocket()}
	// Best-effort: a failed write is one stale status; the next event
	// or the SessionEnd unset corrects it.
	_ = hub.ApplyHook(tm, pane, p, time.Now())
	return 0
}
```

In `main()` (cmd/coop/main.go), before the `isArbiterCmd` dispatch:

```go
	// The hook subcommand is Claude Code calling home on every event;
	// it must stay silent and fast, so it bypasses everything.
	if isHookCmd(os.Args[1:]) {
		os.Exit(runHookCLI(os.Stdin, os.Getenv))
	}
```

Note `cliSocket()` already prefers `COOP_SOCKET`, then the socket path in
`$TMUX` — correct here for the same reason it is for the arbiter verbs.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/coop && go build ./...`
Expected: PASS.

---

### Task 5: `StatusFor` cross-checks; `DeriveStatuses` drops capture

**Files:**
- Modify: `internal/hub/status.go:41-109`
- Modify: `internal/tui/model.go:181` (`DeriveStatuses` call in `poll`)
- Test: `internal/hub/status_test.go` (call sites at lines 76, 95, 111,
  127, 164, 228-263; also `notify_test.go:22`, `done_test.go:24`)

**Interfaces:**
- Consumes: `Pane.Claude` from Task 2.
- Produces: `DeriveStatuses(panes []Pane)` — one parameter. `StatusFor`
  cross-check behavior below. `NeedsInputScreen`/`DialogLine` unchanged
  (arbiter answer gate).

- [ ] **Step 1: Write the failing tests**

Add to `internal/hub/status_test.go`:

```go
func TestStatusForHookCrossChecks(t *testing.T) {
	cases := []struct {
		name   string
		pane   Pane
		want   Status
	}{
		{"hook wins over stale bell", Pane{Bell: true,
			Claude: &ClaudeState{Status: "idle"}}, StatusIdle},
		{"waiting + spinner = approved long tool", Pane{Title: "⠇ npm test",
			Claude: &ClaudeState{Status: "waiting"}}, StatusWorking},
		{"busy + bell = rang since", Pane{Bell: true,
			Claude: &ClaudeState{Status: "busy"}}, StatusNeedsInput},
		{"busy + bell emoji", Pane{Title: "🔔 pick one",
			Claude: &ClaudeState{Status: "busy"}}, StatusNeedsInput},
		{"waiting, no spinner", Pane{Title: "✳ Claude Code",
			Claude: &ClaudeState{Status: "waiting"}}, StatusNeedsInput},
		{"unrecognized status falls to title", Pane{Title: "⠇ working",
			Claude: &ClaudeState{Status: "pondering"}}, StatusWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusFor(tc.pane); got != tc.want {
				t.Errorf("StatusFor = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `go test ./internal/hub -run TestStatusForHookCrossChecks`
Expected: FAIL on "waiting + spinner" and "busy + bell" cases (no
cross-checks yet).

- [ ] **Step 3: Implement**

In `internal/hub/status.go`, replace `StatusFor` and `DeriveStatuses`, and
extract the two title reads:

```go
// titleBell reports the needs-input title signals: tmux's latched bell
// flag or the 🔔 Claude Code puts in the title.
func titleBell(p Pane) bool {
	return p.Bell || strings.Contains(p.Title, "🔔")
}

// titleSpinner reports a braille spinner frame leading the title —
// Claude Code's "working" marker.
func titleSpinner(title string) bool {
	r, _ := utf8.DecodeRuneInString(title)
	return r >= 0x2800 && r <= 0x28FF
}

// StatusFor derives a pane's status. The hook-published state (Claude,
// from the pane's @coop_claude_status options — see hook.go) beats the
// title heuristics, except where the title proves it stale: no hook
// event fires between approving a tool run and its PostToolUse, so
// waiting + spinner means the dialog was answered; and nothing fires on
// an esc-interrupt, so busy + bell means Claude rang for input since.
// Panes publishing nothing (launched outside coop) read from the title
// alone — rules from live observation (2026-07-23): braille spinner =
// working, bell/🔔 = needs input, otherwise idle.
func StatusFor(p Pane) Status {
	if p.Claude != nil {
		if st, ok := p.Claude.status(); ok {
			if st == StatusNeedsInput && titleSpinner(p.Title) {
				return StatusWorking
			}
			if st == StatusWorking && titleBell(p) {
				return StatusNeedsInput
			}
			return st
		}
	}
	if titleBell(p) {
		return StatusNeedsInput
	}
	if titleSpinner(p.Title) {
		return StatusWorking
	}
	return StatusIdle
}

// DeriveStatuses fills each pane's Status — derived fresh every poll,
// never stored.
func DeriveStatuses(panes []Pane) {
	for i := range panes {
		panes[i].Status = StatusFor(panes[i])
	}
}
```

Delete the old capture branch and the `claudeStatusKnown` call inside it.
Keep `dialogOption`, `screenTail`, `ansiRe`, `NeedsInputScreen`,
`StripANSI`, `numberedRow`, `DialogLine` untouched — the arbiter's answer
gate uses them; update `NeedsInputScreen`'s comment to say it serves the
arbiter (`Answer`), no longer the poll.

Update call sites:
- `internal/tui/model.go:181`: `hub.DeriveStatuses(panes)`.
- `status_test.go`, `notify_test.go:22`, `done_test.go:24`: drop the
  second argument at every `DeriveStatuses(panes, …)` call.
- Delete `TestDeriveStatuses` (status_test.go:228) and
  `TestDeriveStatusesSkipsCaptureWithClaudeState` (:257) — they test the
  removed capture path. Keep every `NeedsInputScreen` test.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hub ./internal/tui`
Expected: PASS.

---

### Task 6: Settings generation and launch injection

**Files:**
- Create: `internal/hub/hooksettings.go`
- Modify: `cmd/coop/main.go` (flag block ~189, hub branch ~220-263,
  `createArgv` ~101, `launchIntoTmux` ~146)
- Modify: `scripts/e2e-smoke.sh:37`
- Test: `internal/hub/hooksettings_test.go` (new)

**Interfaces:**
- Consumes: `shellQuote` (internal/hub/arbiter.go:489), `hookAction`'s
  event names.
- Produces: `DefaultHookSettingsPath() string`,
  `WriteHookSettings(path, exe string) error`,
  `WithHookSettings(claudeCmd, path string) string`; a `-hooks` bool flag
  (env `COOP_HOOKS`, default on).

- [ ] **Step 1: Write the failing tests**

Create `internal/hub/hooksettings_test.go`:

```go
package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteHookSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "hooks-settings.json")
	if err := WriteHookSettings(path, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	want := []string{"SessionStart", "UserPromptSubmit", "PostToolUse",
		"PermissionRequest", "PermissionDenied", "Notification", "Stop", "SessionEnd"}
	for _, ev := range want {
		entries := s.Hooks[ev]
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatalf("%s: %d entries", ev, len(entries))
		}
		h := entries[0].Hooks[0]
		if h.Type != "command" || h.Command != "'/home/user/bin/coop' hook" || h.Timeout != 5 {
			t.Errorf("%s hook = %+v", ev, h)
		}
	}
	if len(s.Hooks) != len(want) {
		t.Errorf("hooks for %d events, want %d", len(s.Hooks), len(want))
	}
}

func TestWriteHookSettingsOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-settings.json")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteHookSettings(path, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "stale" {
		t.Error("existing file not regenerated")
	}
}

func TestWithHookSettings(t *testing.T) {
	got := WithHookSettings("claude --continue", "/home/user/x y.json")
	if got != "claude --continue --settings '/home/user/x y.json'" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hub -run 'TestWriteHookSettings|TestWithHookSettings'`
Expected: FAIL — functions undefined.

- [ ] **Step 3: Implement**

Create `internal/hub/hooksettings.go`:

```go
package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// hookEvents is every Claude Code hook event coop hook subscribes to —
// keep in step with hookAction's cases.
var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PostToolUse", "PermissionRequest",
	"PermissionDenied", "Notification", "Stop", "SessionEnd",
}

// DefaultHookSettingsPath is $XDG_STATE_HOME/coop/hooks-settings.json,
// falling back to ~/.local/state (same convention as DefaultAuditPath).
// "" disables injection — a machine with no resolvable home.
func DefaultHookSettingsPath() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "coop", "hooks-settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "coop", "hooks-settings.json")
}

// WriteHookSettings writes the settings file injected into launched
// sessions, registering coop hook for every event. It overwrites
// unconditionally: the file is coop infrastructure, regenerated each
// hub launch so it always names the running binary — deliberately
// unlike ArbiterHome's seed-once user-owned settings.
func WriteHookSettings(path, exe string) error {
	type hookCmd struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"` // seconds; a wedged tmux must not stall the session
	}
	type entry struct {
		Hooks []hookCmd `json:"hooks"`
	}
	hooks := map[string][]entry{}
	for _, ev := range hookEvents {
		hooks[ev] = []entry{{Hooks: []hookCmd{
			{Type: "command", Command: shellQuote(exe) + " hook", Timeout: 5},
		}}}
	}
	raw, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// WithHookSettings appends the --settings flag to claudeCmd, which may
// carry its own flags ("claude --continue") — ours append after, like
// ArbiterCmd's.
func WithHookSettings(claudeCmd, path string) string {
	return claudeCmd + " --settings " + shellQuote(path)
}
```

`cmd/coop/main.go` — add the flag with the others:

```go
	hooks := flag.Bool("hooks", envOr("COOP_HOOKS", "1") != "0",
		"inject status-publishing hooks into sessions created from the picker")
```

Propagate it through the launcher: `createArgv` gains a `hooks bool`
parameter and appends `"-hooks="+strconv.FormatBool(hooks)` right after
the `-done-ttl` value in the inner coop argv; `launchIntoTmux` gains the
same parameter and passes it through; the `main()` call passes `*hooks`.
(Import `strconv` in main.go.)

In the hub branch (after the `ApplyHubStyle` call, before `tui.New`),
compose the effective claude command:

```go
	// Inject the status-publishing hooks into every session this hub
	// launches. Best-effort: a failed write just means new sessions run
	// on the title fallback, same as sessions from before the upgrade.
	launchCmd := *claudeCmd
	if *hooks {
		if p := hub.DefaultHookSettingsPath(); p != "" {
			if exe, err := os.Executable(); err == nil {
				if werr := hub.WriteHookSettings(p, exe); werr == nil {
					launchCmd = hub.WithHookSettings(*claudeCmd, p)
				} else {
					fmt.Fprintln(os.Stderr, "coop: hook settings:", werr)
				}
			}
		}
	}
```

and pass `launchCmd` (not `*claudeCmd`) into `tui.New` — the arbiter
launch path reads the same Model field, so `ArbiterCmd` inherits the flag
with no change.

`scripts/e2e-smoke.sh:37` — the fake claude is `sleep 300`, which trailing
flags would break; disable injection:

```
  "/tmp/coop-e2e -socket '$SOCKET' -allowed-cmds sleep,sh,bash -config '$TMPD/config.json' -claude-cmd 'sleep 300' -hooks=false"
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hub && go build ./...`
Expected: PASS.

---

### Task 7: Delete the pid-file tier

**Files:**
- Modify: `internal/hub/claudestate.go` (shrink to struct + `status()` +
  `Since`)
- Delete: `internal/hub/claudestate_test.go`
- Modify: `internal/hub/arbiter.go:370-392` (`Peek`)
- Modify: `cmd/coop/arbitercli.go:81-83` (`Peek` call)
- Modify: `internal/tui/model.go` (field ~69, `New` ~132, `poll` ~158+179)
- Test: existing suites; grep-driven cleanup

**Interfaces:**
- Consumes: `Pane.Claude` now populated solely by `parsePanes` (Task 2).
- Produces: `Peek(tm Tmux, tr *Transcripts, session string) (string, error)`
  — the `*ClaudeSessions` parameter is gone. `ClaudeState` loses `Name`
  and `Version` (write-only fields). Deleted symbols: `ClaudeSessions`,
  `DefaultClaudeSessions`, `Lookup`, `procStartTime`, `AttachClaudeState`,
  `claudeStatusKnown`, `claudeStateFile`.

- [ ] **Step 1: Shrink claudestate.go**

Replace the whole file with:

```go
package hub

import "time"

// ClaudeState is the state a session's injected coop hooks published
// onto its pane — @coop_claude_status and friends, written by
// ApplyHook, read back by parsePanes. Nil on panes publishing nothing
// (sessions launched outside coop, or from before hook injection),
// which fall back to the title heuristics in StatusFor.
type ClaudeState struct {
	SessionID   string    // names the transcript, ~/.claude/projects/<slug>/<id>.jsonl
	Status      string    // "busy" | "waiting" | "idle"
	StatusSince time.Time // when it entered Status (zero if unpublished)
	CWD         string
}

// status maps the published status onto ours. The bool is false for
// anything unrecognized — a value a later coop wrote — so callers fall
// back to the title rather than mislabel a busy pane idle.
func (s *ClaudeState) status() (Status, bool) {
	switch s.Status {
	case "busy":
		return StatusWorking, true
	case "waiting":
		return StatusNeedsInput, true
	case "idle":
		return StatusIdle, true
	}
	return StatusIdle, false
}

// Since is when the pane entered its current status: the hook-published
// since where there is one, else the session's start time. "waiting 6m"
// is what makes a row worth switching to; "session started 3h ago" says
// nothing about whether it needs you.
func (p Pane) Since() time.Time {
	if p.Claude != nil && !p.Claude.StatusSince.IsZero() {
		return p.Claude.StatusSince
	}
	return p.Created
}
```

Delete `internal/hub/claudestate_test.go` entirely (it tests `Lookup` /
`procStartTime`; the `status()` mapping is covered by
`TestStatusForHookCrossChecks`, including the unrecognized-status case).
Also remove the `AttachClaudeState` guard added in Task 2 — the function
is gone.

- [ ] **Step 2: Update Peek and its caller**

`internal/hub/arbiter.go` — signature drops `cs *ClaudeSessions`; the
transcript lookup reads the pane's parsed state:

```go
func Peek(tm Tmux, tr *Transcripts, session string) (string, error) {
```

and replace the `cs.Lookup(p.PID)` block with:

```go
	if c := p.Claude; c != nil && c.SessionID != "" {
		if text, ok := tr.LastText(c.SessionID, c.CWD); ok {
			fmt.Fprintf(&b, "\n=== last assistant message ===\n%s\n", text)
		}
	}
```

`cmd/coop/arbitercli.go:81-82`:

```go
		if out, err = hub.Peek(tm, hub.DefaultTranscripts(), pos[0]); err == nil {
```

- [ ] **Step 3: Update the tui Model**

`internal/tui/model.go`:
- Delete the `claude *hub.ClaudeSessions` field (~line 69) and the
  `claude: hub.DefaultClaudeSessions(),` initializer in `New` (~132).
- In `poll` (~158): `claude, transcripts := m.claude, m.transcripts`
  becomes `transcripts := m.transcripts`; delete the
  `hub.AttachClaudeState(panes, claude.Lookup)` line (~179).

- [ ] **Step 4: Sweep for leftovers and verify**

```bash
grep -rn "ClaudeSessions\|AttachClaudeState\|procStartTime\|claudeStatusKnown\|claudeStateFile" --include="*.go" .
```

Expected: no hits (fix any stragglers — likely `arbiter_test.go` or
`model_test.go` constructing the old types; rewrite those tests to set
`Pane.Claude` directly instead of going through `Lookup`).

Run: `go test ./...`
Expected: PASS.

---

### Task 8: Docs and end-to-end verification

**Files:**
- Modify: `CLAUDE.md` ("Core design" status list, arbiter section's CLI
  list, transcript section's sessionId mention)
- Verify: full suite + `scripts/e2e-smoke.sh` + live manual pass

- [ ] **Step 1: Rewrite CLAUDE.md's status derivation section**

Replace the three-source list under "Core design: tmux is the database"
with the two-tier reality, covering: sessions launched from the picker get
`--settings $XDG_STATE_HOME/coop/hooks-settings.json` (regenerated every
hub launch, appended to `claudeCmd` — so `ArbiterCmd` inherits it);
`coop hook` publishes `@coop_claude_status` / `@coop_status_since` /
`@coop_session_id` / `@coop_claude_cwd` onto its pane via `$TMUX_PANE`;
the poll reads them through `paneFormat`; tier 2 is the pane-title
heuristic (spinner/bell); the two `StatusFor` cross-checks and why
(approved-long-tool and esc-interrupt event gaps); the `-hooks=false`
opt-out; and that the pid-file tier (`~/.claude/sessions/<pid>.json`) is
gone. Update the transcript section: the `sessionId` now comes from
`@coop_session_id`, not "the pane's published state" file. Add `hook` to
the list of pre-TUI subcommands in the arbiter section ("dispatched in
`main.go` before the TUI ever starts"). Keep the file's voice — dense
prose explaining why, not a changelog.

- [ ] **Step 2: Full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 3: e2e smoke**

Run: `scripts/e2e-smoke.sh`
Expected: PASS (it exercises launch plumbing, which now carries
`-hooks=false` for its fake claude).

- [ ] **Step 4: Live manual pass (operator-visible, report results)**

Build and run against a real claude on a throwaway socket:

```bash
go build -o /tmp/coop-hooks ./cmd/coop
COOP_SOCKET=coophooks /tmp/coop-hooks
```

In the TUI: create a session from the picker (`n`); confirm the row shows
`idle` then `working` after typing a prompt, `NEEDS INPUT` on a permission
dialog (and on an AskUserQuestion dialog if convenient), `working` again
after approving. Then verify the options directly:

```bash
tmux -L coophooks list-panes -a -F '#{pane_id} #{@coop_claude_status} #{@coop_session_id}'
```

Expected: the claude pane shows a live status and session id. Kill the
socket afterwards: `tmux -L coophooks kill-server`. Report what was
observed — including any event the mapping missed (esc-interrupt staleness
is expected to linger ≤60s until `idle_prompt`; that is by design, not a
bug).

---

## Self-Review (completed)

- **Spec coverage:** options table → Tasks 2/3; event mapping → Task 3;
  cross-checks → Task 5; `coop hook` discipline → Task 4; settings
  regeneration + `--settings` injection + arbiter inheritance → Task 6;
  deletions (`ClaudeSessions`, capture fallback) with `NeedsInputScreen`
  kept → Tasks 5/7; spike → Task 1; testing policy → per-task; docs →
  Task 8. The spec's `-hooks` opt-out is plan-added (e2e's fake claude
  can't take flags) — flag it in the whole-branch review.
- **Placeholder scan:** none; every step has runnable code or exact
  commands.
- **Type consistency:** `HookPayload`/`ParseHookPayload`/`ApplyHook`
  (Tasks 3→4), marker constants (2→3→7), `WithHookSettings` (6),
  `Peek(tm, tr, session)` (7), `DeriveStatuses(panes)` (5) — names match
  across tasks.
