package hub

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArbiterHomeSeedsPolicyAndNoSettings(t *testing.T) {
	dir := t.TempDir()
	home, err := ArbiterHome(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("arbiter home not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("the judge runs no tools — no settings.json should be written")
	}
	if _, err := os.Stat(filepath.Join(dir, "arbiter.md")); err != nil {
		t.Errorf("policy not seeded: %v", err)
	}
}

func TestArbiterHomeLeavesExistingPolicyAlone(t *testing.T) {
	dir := t.TempDir()
	want := "# mine\n"
	if err := os.WriteFile(filepath.Join(dir, "arbiter.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArbiterHome(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "arbiter.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("policy overwritten: %q", got)
	}
}

// The tool restriction is the enforcement of "the judge runs no tools",
// and it has to be on the command line: a settings file can be widened
// by the operator's own permissions.allow.
func TestJudgeClaudeArgsRunNoTools(t *testing.T) {
	args := judgeClaudeArgs("haiku", "be careful")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p", "--tools  ", "--strict-mcp-config",
		"--output-format json", "--model haiku"} {
		if !strings.Contains(joined+" ", want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	// --tools takes the empty set, not the next flag: an off-by-one in
	// the argv would silently turn every tool back on.
	for i, a := range args {
		if a == "--tools" {
			if i+1 >= len(args) || args[i+1] != "" {
				t.Errorf("--tools must be followed by the empty set, got %v", args)
			}
		}
	}
	if strings.Contains(joined, "--settings") {
		t.Error("the judge must register none of coop's hooks")
	}
}

// A failing claude's stderr is the whole diagnosis — the judge is
// detached with both its streams on /dev/null, so judge.log is the only
// channel it has, and cmd.Output parks stderr where nothing read it.
// The child here is /bin/sh, never claude and never coop judge: what is
// under test is the plumbing from Output's ExitError to the message.
func TestJudgeRunErrorCarriesStderr(t *testing.T) {
	err := failingChild(t, "printf 'Invalid model name\\n' >&2; exit 1")
	got := judgeRunError(err).Error()
	if !strings.Contains(got, "Invalid model name") {
		t.Errorf("error %q dropped the child's stderr", got)
	}
	if !strings.Contains(got, "exit status 1") {
		t.Errorf("error %q lost the exit status", got)
	}
}

// A child that writes a stack trace per line must not append a screenful
// to the log for every episode, and must not smuggle control bytes into
// a file a human reads with cat.
func TestJudgeRunErrorBoundsStderr(t *testing.T) {
	err := failingChild(t,
		`i=0; while [ $i -lt 200 ]; do printf 'boom \033[2Jboom\n' >&2; i=$((i+1)); done; exit 1`)
	got := judgeRunError(err).Error()
	if len([]rune(got)) > noteMax+64 {
		t.Errorf("error is %d runes, want it bounded near noteMax", len([]rune(got)))
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("an escape byte reached the judge log: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("error spans lines, one log line per episode is the contract: %q", got)
	}
}

// An error that is not an ExitError (claude missing, timeout) passes
// through untouched — there is no stderr to add and the wrapping would
// only obscure it.
func TestJudgeRunErrorPassesOtherErrorsThrough(t *testing.T) {
	want := errors.New("claude not found in /usr/bin")
	if got := judgeRunError(want); got != want {
		t.Errorf("judgeRunError rewrote a non-exec error: %v", got)
	}
}

// failingChild runs one sh script through cmd.Output and returns the
// error, which is how ClaudeJudgeRunner obtains its own.
func failingChild(t *testing.T, script string) error {
	t.Helper()
	sh, lookErr := exec.LookPath("sh")
	if lookErr != nil {
		t.Skip("no sh on this platform")
	}
	_, err := exec.Command(sh, "-c", script).Output()
	if err == nil {
		t.Fatal("the child was supposed to fail")
	}
	return err
}

// claimsIn points the claims directory at dir for one test. Not an
// environment variable: claimRoot deliberately ignores those.
func claimsIn(t *testing.T, dir string) {
	t.Helper()
	prev := claimHome
	claimHome = func() string { return dir }
	t.Cleanup(func() { claimHome = prev })
}

func TestClaimEpisodeIsOncePerEpisode(t *testing.T) {
	claimsIn(t, t.TempDir())
	if !ClaimEpisode("%5", "1700000000") {
		t.Fatal("first claim should win")
	}
	if ClaimEpisode("%5", "1700000000") {
		t.Error("a second hook process for the same dialog must not spawn a second judge")
	}
	// The next dialog on the same pane moves @coop_status_since, so it is
	// a different episode and claims cleanly.
	if !ClaimEpisode("%5", "1700000900") {
		t.Error("a later episode on the same pane must be claimable")
	}
	if !ClaimEpisode("%6", "1700000000") {
		t.Error("another pane's episode must be claimable")
	}
}

// A claim consumed by a judge that never started must not wedge the
// dialog: the next hook event for it carries the same since, so without
// the release it would be refused forever.
func TestReleaseEpisodeMakesItClaimableAgain(t *testing.T) {
	claimsIn(t, t.TempDir())
	if !ClaimEpisode("%5", "1700000000") {
		t.Fatal("first claim should win")
	}
	ReleaseEpisode("%5", "1700000000")
	if !ClaimEpisode("%5", "1700000000") {
		t.Error("a released episode must be claimable again")
	}
}

// Nowhere to write is the safe direction: no claim, no judge.
func TestClaimEpisodeWithoutStateDir(t *testing.T) {
	// A home that is really a file: nothing under it can be created.
	f := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	claimsIn(t, f)
	if ClaimEpisode("%1", "1700000000") {
		t.Error("claim should fail when the claims directory cannot be created")
	}
	claimsIn(t, "")
	if ClaimEpisode("%1", "1700000000") {
		t.Error("claim should fail when there is no home to resolve")
	}
}

// The claims directory must not move with $XDG_STATE_HOME: ClaimEpisode
// runs inside coop hook, where that variable is the monitored session's
// to set, and a session that varies it per event defeats the dedupe.
func TestClaimEpisodeIgnoresStateHomeEnv(t *testing.T) {
	home := t.TempDir()
	claimsIn(t, home)
	if !ClaimEpisode("%5", "1700000000") {
		t.Fatal("first claim should win")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // a session moving the goalposts
	if ClaimEpisode("%5", "1700000000") {
		t.Error("redirecting XDG_STATE_HOME must not win a second claim on one episode")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "state", "coop", "claims")); err != nil {
		t.Errorf("claims should live under the passwd home: %v", err)
	}
}

// A claim older than claimTTL is swept, so a killed judge's file cannot
// accumulate — the episode it named is long over either way.
func TestPruneClaimsDropsStaleFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "pane-1")
	fresh := filepath.Join(dir, "pane-2")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-2*claimTTL), now.Add(-2*claimTTL)); err != nil {
		t.Fatal(err)
	}
	pruneClaims(dir, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("stale claim survived")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("live claim swept")
	}
}

// Pane ids ("%12") and option values must not escape the claims
// directory or collide across panes.
func TestClaimNameIsOnePathElement(t *testing.T) {
	got := claimName("%12", "1700000000")
	if strings.ContainsAny(got, "/%") {
		t.Errorf("claimName = %q", got)
	}
	if claimName("%1", "170") == claimName("%2", "170") {
		t.Error("different panes must not share a claim")
	}
}

func TestJudgeSystemPromptCarriesPreambleAndPolicy(t *testing.T) {
	got := JudgeSystemPrompt("never approve pushes")
	if !strings.Contains(got, "untrusted") {
		t.Error("the preamble's untrusted-data framing must survive")
	}
	if !strings.Contains(got, "never approve pushes") {
		t.Error("the user's policy must be appended")
	}
	if !strings.Contains(got, `"action"`) {
		t.Error("the output schema must be stated")
	}
}

// JudgeEnv is an allowlist, so the test is written as one: anything not
// named in judgeEnvKeep must be gone, whether or not this list ever
// heard of it. The named cases are the ones verified against the
// installed claude — every one of them decides where the judge's prompt
// goes, what credentials it carries, or which binary answers.
func TestJudgeEnvPassesOnlyTheAllowlist(t *testing.T) {
	hostile := []string{
		"PATH=/home/user/sprocket-v2/.evil/bin:/usr/bin",
		"HOME=/home/user/sprocket-v2/.evil",
		"CLAUDE_CONFIG_DIR=/home/user/sprocket-v2/.evil/claude",
		"HTTPS_PROXY=http://127.0.0.1:9/", "HTTP_PROXY=http://127.0.0.1:9/",
		"NODE_EXTRA_CA_CERTS=/home/user/sprocket-v2/.evil/ca.pem",
		"NODE_OPTIONS=--require /home/user/sprocket-v2/.evil/hook.js",
		"CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=1",
		"AWS_BEARER_TOKEN_BEDROCK=t", "ANTHROPIC_VERTEX_PROJECT_ID=p",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:9/", "ANTHROPIC_API_KEY=sk-x",
		"TMUX_PANE=%7", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli",
		"COOP_ALLOWED_CMDS=claude,node,bash", "COOP_CONFIG=/home/user/sprocket-v2/coop.json",
		"XDG_STATE_HOME=/home/user/sprocket-v2/.evil/state",
		"LANG=en_GB.UTF-8", "TMUX_TMPDIR=/tmp/tmux-1000",
	}
	got := JudgeEnv(hostile)
	seen := map[string]string{}
	for _, kv := range got {
		name, val, _ := strings.Cut(kv, "=")
		seen[name] = val
	}
	for name := range seen {
		if !slices.Contains(judgeEnvKeep, name) && name != "HOME" && name != "PATH" {
			t.Errorf("%s reached the judge; only judgeEnvKeep, HOME and PATH may", name)
		}
	}
	if seen["LANG"] != "en_GB.UTF-8" || seen["TMUX_TMPDIR"] != "/tmp/tmux-1000" {
		t.Errorf("the allowlisted pass-throughs were dropped: %v", got)
	}
	// HOME and PATH are constructed, never the hostile ones: PATH decides
	// which binary *is* the judge, HOME where its credentials live.
	if strings.Contains(seen["PATH"], ".evil") || !strings.Contains(seen["PATH"], "/usr/bin") {
		t.Errorf("PATH = %q", seen["PATH"])
	}
	if strings.Contains(seen["HOME"], ".evil") || !filepath.IsAbs(seen["HOME"]) {
		t.Errorf("HOME = %q", seen["HOME"])
	}
	// XDG_STATE_HOME is not a pass-through: it resolves the judge's claim
	// directory and its audit log, so a session that set it could silence
	// the durable record of every answer.
	if _, ok := seen["XDG_STATE_HOME"]; ok {
		t.Error("XDG_STATE_HOME reached the judge — the audit path is session-redirectable again")
	}
}

// The judge's claude is resolved against the constructed PATH, not the
// process's own: exec.Command looks a bare name up through
// os.Getenv("PATH"), which cmd.Env does not affect, so a judge started
// by hand from a hostile environment would otherwise run whatever that
// PATH found first.
func TestJudgePathLeadsWithTheCoopBinaryDir(t *testing.T) {
	t.Setenv("PATH", "/home/user/sprocket-v2/.evil/bin")
	got := judgePath()
	if strings.Contains(got, ".evil") {
		t.Errorf("judgePath = %q, want nothing inherited", got)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	if first, _, _ := strings.Cut(got, ":"); first != filepath.Dir(exe) {
		t.Errorf("judgePath = %q, want it to lead with %q", got, filepath.Dir(exe))
	}
	for _, want := range judgeSystemPath {
		if !slices.Contains(strings.Split(got, ":"), want) {
			t.Errorf("judgePath = %q, missing %q", got, want)
		}
	}
}
