package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"coop/internal/config"
	"coop/internal/hub"
)

func TestIsJudgeCmd(t *testing.T) {
	if !isJudgeCmd([]string{"judge", "%1"}) {
		t.Error("judge should be recognized")
	}
	for _, args := range [][]string{nil, {"hook"}, {"peek", "alpha"}, {"-socket", "coop"}} {
		if isJudgeCmd(args) {
			t.Errorf("%v should not select judge", args)
		}
	}
}

func TestJudgeArgvShape(t *testing.T) {
	got := judgeArgv("/usr/local/bin/coop", "coop", "%1", "Bash: go test ./...", "Explore")
	want := []string{"/usr/local/bin/coop", "judge", "-socket", "coop",
		"-detail", "Bash: go test ./...", "-agent", "Explore", "%1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The judge's allowlist is the operator's file, never a flag and never
// the environment — and an explicitly empty list is a setting, not an
// omission: it means send nothing, the same as -allowed-cmds "" already
// means on the TUI's side of the same gate.
func TestJudgeAllowedCmds(t *testing.T) {
	cfg := func(list []string) config.Config {
		var c config.Config
		c.Arbiter.AllowedCmds = list
		return c
	}
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"absent falls back to the built-in", nil, []string{"claude", "node"}},
		{"explicitly empty sends nothing", []string{}, nil},
		{"blank entries are not a list", []string{"", "  "}, nil},
		{"operator's list wins", []string{"claude, node", "bun"},
			[]string{"claude", "node", "bun"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := judgeAllowedCmds(cfg(tc.in)); !slices.Equal(got, tc.want) {
				t.Errorf("judgeAllowedCmds(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// A judge whose config file is present but unparseable must not regain
// the built-in send gate: an operator who wrote allowed_cmds [] ("never
// send") and later broke the JSON — or wrote a bare string where the
// list belongs, which fails the whole Config unmarshal — would otherwise
// have every judge silently answering dialogs again, with nothing in the
// log to say so.
func TestJudgeConfigFailsClosedOnAnUnparseableFile(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"syntax error", `{"arbiter": {"allowed_cmds": []},}`},
		{"string where a list belongs", `{"arbiter": {"allowed_cmds": "claude"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			var logged []string
			model, allowed := judgeConfig(path, func(f string, a ...any) {
				logged = append(logged, fmt.Sprintf(f, a...))
			})
			if allowed != nil {
				t.Errorf("allowed = %v, want no send gate at all", allowed)
			}
			if model != hub.DefaultArbiterModel {
				t.Errorf("model = %q, want the default", model)
			}
			if len(logged) == 0 {
				t.Error("an unparseable config must say so in the judge log")
			}
		})
	}
}

// A config that is simply absent is not an error: the operator never
// wrote one, and the built-in defaults are what they get.
func TestJudgeConfigMissingFileFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var logged []string
	model, allowed := judgeConfig(path, func(f string, a ...any) {
		logged = append(logged, fmt.Sprintf(f, a...))
	})
	if !slices.Equal(allowed, []string{"claude", "node"}) {
		t.Errorf("allowed = %v, want the built-in list", allowed)
	}
	if model != hub.DefaultArbiterModel {
		t.Errorf("model = %q, want the default", model)
	}
	if len(logged) != 0 {
		t.Errorf("a missing config is not a failure worth logging: %v", logged)
	}
}

// A file that parses is obeyed, including the empty list that means
// "never send".
func TestJudgeConfigHonoursTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"arbiter": {"model": "sonnet", "allowed_cmds": []}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	model, allowed := judgeConfig(path, func(string, ...any) {})
	if model != "sonnet" || allowed != nil {
		t.Errorf("model = %q allowed = %v, want sonnet and no send gate", model, allowed)
	}
}

// gateTmux doubles the two tmux reads the spawn gate makes.
type gateTmux struct {
	globals map[string]string
	opts    map[string]string // "pane/name" -> value
	optErr  error             // PaneOption failure, e.g. a pane that just died
}

func (g *gateTmux) GlobalOption(name string) (string, error) { return g.globals[name], nil }
func (g *gateTmux) PaneOption(pane, name string) (string, error) {
	if g.optErr != nil {
		return "", g.optErr
	}
	return g.opts[pane+"/"+name], nil
}

// waitingTmux is a pane mid-episode: the arbiter is on, and ApplyHook has
// just published waiting with the since that names this episode.
func waitingTmux(mode string) *gateTmux {
	return &gateTmux{
		globals: map[string]string{hub.ArbiterModeMarker: mode},
		opts:    map[string]string{"%1/" + hub.ClaudeSinceMarker: "1700000000"},
	}
}

func permissionRequest() hub.HookPayload {
	p := hub.HookPayload{Event: "PermissionRequest", ToolName: "Bash"}
	p.ToolInput.Command = "go test ./..."
	return p
}

// captureSpawns replaces the real spawn for the duration of a test and
// returns the record of what would have been started.
func captureSpawns(t *testing.T, err error) *[][]string {
	t.Helper()
	var argvs [][]string
	prev := judgeSpawn
	judgeSpawn = func(argv, env []string) error {
		argvs = append(argvs, argv)
		return err
	}
	t.Cleanup(func() { judgeSpawn = prev })
	return &argvs
}

func TestSpawnJudgeGates(t *testing.T) {
	cases := []struct {
		name string
		tm   *gateTmux
		p    hub.HookPayload
	}{
		// Only a needs-input episode is judged; a Stop is just a row
		// going idle.
		{"not waiting", waitingTmux(hub.ArbiterModeRecommend), hub.HookPayload{Event: "Stop"}},
		// Judging is opt-in: no mode, no claude -p, ever.
		{"mode off", waitingTmux(hub.ArbiterModeOff), permissionRequest()},
		// No episode key (ApplyHook's write failed): skipping beats
		// claiming the pane itself and wedging it until the claim ages out.
		{"no since", &gateTmux{globals: map[string]string{
			hub.ArbiterModeMarker: hub.ArbiterModeFull}}, permissionRequest()},
		{"since unreadable", func() *gateTmux {
			g := waitingTmux(hub.ArbiterModeFull)
			g.optErr = errors.New("pane gone")
			return g
		}(), permissionRequest()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClaims(t)
			argvs := captureSpawns(t, nil)
			spawnJudge(tc.tm, "coop", "%1", tc.p)
			if len(*argvs) != 0 {
				t.Errorf("spawned %v", *argvs)
			}
		})
	}
}

// fakeClaims swaps the episode claim for an in-memory one. The real
// claims directory ignores $XDG_STATE_HOME by design (hub.claimRoot — a
// monitored session can set that variable, so honouring it would hand
// the session the dedupe), which means a test cannot redirect it with
// the environment, and must not fall back to writing into the
// developer's own home. hub's own tests cover the real filesystem.
func fakeClaims(t *testing.T) {
	t.Helper()
	held := map[string]bool{}
	key := func(pane, since string) string { return pane + "\x00" + since }
	pc, pr := claimEpisode, releaseEpisode
	claimEpisode = func(pane, since string) bool {
		if held[key(pane, since)] {
			return false
		}
		held[key(pane, since)] = true
		return true
	}
	releaseEpisode = func(pane, since string) { delete(held, key(pane, since)) }
	t.Cleanup(func() { claimEpisode, releaseEpisode = pc, pr })
}

// unclaimable makes every claim fail, standing in for a claims directory
// that cannot be written.
func unclaimable(t *testing.T) {
	t.Helper()
	pc, pr := claimEpisode, releaseEpisode
	claimEpisode = func(string, string) bool { return false }
	releaseEpisode = func(string, string) {}
	t.Cleanup(func() { claimEpisode, releaseEpisode = pc, pr })
}

func TestSpawnJudgeSpawnsOncePerEpisode(t *testing.T) {
	fakeClaims(t)
	// The judged session's environment must not reach the judge: this is
	// the gate a widened allowlist would ride in on.
	t.Setenv("COOP_ALLOWED_CMDS", "claude,node,bash")
	var envs [][]string
	prev := judgeSpawn
	judgeSpawn = func(argv, env []string) error { envs = append(envs, env); return nil }
	t.Cleanup(func() { judgeSpawn = prev })

	tm := waitingTmux(hub.ArbiterModeFull)
	spawnJudge(tm, "coop", "%1", permissionRequest())
	if len(envs) != 1 {
		t.Fatalf("spawned %d judges, want 1", len(envs))
	}
	if i := slices.IndexFunc(envs[0], func(kv string) bool {
		return strings.HasPrefix(kv, "COOP_ALLOWED_CMDS=")
	}); i >= 0 {
		t.Errorf("the judged session's allowlist reached the judge: %q", envs[0][i])
	}

	// A second hook process for the same dialog — a Notification about
	// the PermissionRequest above — must not spawn a second judge, or two
	// digits land in one dialog.
	spawnJudge(tm, "coop", "%1", hub.HookPayload{
		Event: "Notification", NotificationType: "permission_prompt"})
	if len(envs) != 1 {
		t.Errorf("two judges for one episode: %d spawns", len(envs))
	}

	// The next dialog moves @coop_status_since, which is what makes the
	// pane eligible again.
	tm.opts["%1/"+hub.ClaudeSinceMarker] = "1700000900"
	spawnJudge(tm, "coop", "%1", permissionRequest())
	if len(envs) != 2 {
		t.Errorf("a fresh episode was not judged: %d spawns", len(envs))
	}
}

// A judge that never started must leave the episode claimable: the next
// hook event for the same dialog carries the same since, so a claim
// consumed by a failed fork would wedge that dialog for good.
func TestSpawnJudgeReleasesTheClaimWhenStartFails(t *testing.T) {
	fakeClaims(t)
	fail := true
	var argvs [][]string
	prev := judgeSpawn
	judgeSpawn = func(argv, env []string) error {
		argvs = append(argvs, argv)
		if fail {
			return errors.New("fork failed")
		}
		return nil
	}
	t.Cleanup(func() { judgeSpawn = prev })

	tm := waitingTmux(hub.ArbiterModeFull)
	spawnJudge(tm, "coop", "%1", permissionRequest())
	fail = false
	spawnJudge(tm, "coop", "%1", permissionRequest())
	if len(argvs) != 2 {
		t.Errorf("a failed spawn wedged the episode: %d attempts, want 2", len(argvs))
	}
}

// The argv carries the socket the hook found in $TMUX and the event's
// substance, so the judge lands on the right server with the trigger in
// its prompt.
func TestSpawnJudgeArgvCarriesSocketAndDetail(t *testing.T) {
	fakeClaims(t)
	argvs := captureSpawns(t, nil)
	spawnJudge(waitingTmux(hub.ArbiterModeRecommend), "coop-e2e", "%1", permissionRequest())
	if len(*argvs) != 1 {
		t.Fatalf("spawned %d judges", len(*argvs))
	}
	got := strings.Join((*argvs)[0], " ")
	for _, want := range []string{"judge", "-socket coop-e2e", "Bash: go test ./...", "%1"} {
		if !strings.Contains(got, want) {
			t.Errorf("argv %q missing %q", got, want)
		}
	}
}

// The subagent an event came from only exists in the hook payload — the
// judge cannot recover it from the transcript, whose format is
// documented as internal and unstable — so the spawn has to carry it,
// and a main-thread request must carry nothing.
func TestSpawnJudgeArgvCarriesTheSubagent(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		p          hub.HookPayload
	}{
		{"subagent", "general-purpose", func() hub.HookPayload {
			p := permissionRequest()
			p.AgentID, p.AgentType = "ag_1", "general-purpose"
			return p
		}()},
		{"main thread", "", permissionRequest()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeClaims(t)
			argvs := captureSpawns(t, nil)
			spawnJudge(waitingTmux(hub.ArbiterModeFull), "coop", "%1", tc.p)
			if len(*argvs) != 1 {
				t.Fatalf("spawned %d judges", len(*argvs))
			}
			argv := (*argvs)[0]
			i := slices.Index(argv, "-agent")
			if i < 0 || i+1 >= len(argv) {
				t.Fatalf("argv %v has no -agent", argv)
			}
			if argv[i+1] != tc.want {
				t.Errorf("-agent %q, want %q", argv[i+1], tc.want)
			}
		})
	}
}

// Nowhere to claim is the safe direction: no claim, no judge. What makes
// a claim fail for real — an unwritable claims directory, no resolvable
// home — is hub's to test; what matters here is that a refused claim
// stops the spawn.
func TestSpawnJudgeSkipsWhenTheClaimCannotBeTaken(t *testing.T) {
	unclaimable(t)
	argvs := captureSpawns(t, nil)
	spawnJudge(waitingTmux(hub.ArbiterModeFull), "coop", "%1", permissionRequest())
	if len(*argvs) != 0 {
		t.Errorf("spawned %v with nowhere to claim the episode", *argvs)
	}
}
