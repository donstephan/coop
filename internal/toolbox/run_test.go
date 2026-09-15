package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func argLine(args []string) string { return strings.Join(args, " ") }

func TestRunArgsMountsRepoAtHostPath(t *testing.T) {
	fakeHome(t)
	repo := "/home/user/sprocket-v2"
	args, err := RunArgs(repo, "coop-tools:base-x", RepoConfig{}, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	line := argLine(args)
	if !strings.Contains(line, "-v "+repo+":"+repo+":rw") {
		t.Errorf("repo not mounted at its host path: %s", line)
	}
	if !strings.Contains(line, "--name "+ContainerName(repo)) {
		t.Errorf("container not named: %s", line)
	}
	if !strings.Contains(line, "--rm") {
		t.Errorf("--rm missing, an idled-out container would linger: %s", line)
	}
	if !strings.Contains(line, "-v /tmp:/tmp:rw") {
		t.Errorf("/tmp not mounted: %s", line)
	}
	if !strings.Contains(line, "-v "+HomeDir(repo)+":"+HomeDir(repo)+":rw") {
		t.Errorf("toolbox home not mounted: %s", line)
	}
	if !strings.Contains(line, "-e HOME="+HomeDir(repo)) {
		t.Errorf("HOME not set to the toolbox home: %s", line)
	}
	for _, v := range []string{"GOCACHE", "PIP_CACHE_DIR", "npm_config_cache", "CARGO_HOME"} {
		if !strings.Contains(line, "-e "+v+"="+HomeDir(repo)) {
			t.Errorf("%s does not resolve under the persisted home: %s", v, line)
		}
	}
	if strings.Contains(line, "docker.sock") {
		t.Fatalf("the docker socket must never be mounted: %s", line)
	}
	if !strings.Contains(line, "--label coop.managed=true") {
		t.Errorf("management label missing: %s", line)
	}
}

func TestRunArgsForwardsAlmostNoEnvironment(t *testing.T) {
	fakeHome(t)
	// "The container inherits almost no environment" is a design
	// decision, not an accident of which variables happened to be set:
	// a credential in the operator's shell must not become a credential
	// in every repo's container.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leaked-secret-value")
	t.Setenv("TERM", "xterm-256color")
	args, err := RunArgs("/home/user/sprocket-v2", "img", RepoConfig{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	line := argLine(args)
	if strings.Contains(line, "AWS_SECRET_ACCESS_KEY") || strings.Contains(line, "leaked-secret-value") {
		t.Errorf("an unlisted variable was forwarded: %s", line)
	}
	if !strings.Contains(line, "-e TERM=xterm-256color") {
		t.Errorf("TERM should be forwarded: %s", line)
	}
}

func TestInspect(t *testing.T) {
	fakeHome(t)
	repo := "/home/user/sprocket-v2"
	e := newFakeEngine()
	key := "inspect -f {{.State.Running}} {{.Config.Image}} " + ContainerName(repo)
	e.outputs[key] = "true coop-tools-x:abc\n"
	st := Inspect(e, repo)
	if !st.Running || st.Image != "coop-tools-x:abc" {
		t.Errorf("Inspect = %+v", st)
	}
	if !Running(e, repo) {
		t.Error("Running should agree with Inspect")
	}
	// No such container: the engine errors, and that reads as nothing
	// usable rather than as a half-filled state.
	e2 := newFakeEngine()
	e2.errs[key] = errNoImage{}
	if st := Inspect(e2, repo); st.Running || st.Image != "" {
		t.Errorf("absent container = %+v, want zero", st)
	}
}

func TestRunArgsNetworkAndMounts(t *testing.T) {
	home := fakeHome(t)
	// The directory has to be there: a declared path this host does not
	// have is skipped rather than mounted.
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := RepoConfig{Network: "sprocket-v2_default", Mounts: []string{"~/.local/bin:rw"}}
	args, err := RunArgs("/home/user/sprocket-v2", "img", cfg, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	line := argLine(args)
	if !strings.Contains(line, "--network sprocket-v2_default") {
		t.Errorf("network missing: %s", line)
	}
	bin := filepath.Join(home, ".local", "bin")
	if !strings.Contains(line, "-v "+bin+":"+bin+":rw") {
		t.Errorf("declared mount missing: %s", line)
	}
}

func TestRunArgsRejectsBadMount(t *testing.T) {
	fakeHome(t)
	cfg := RepoConfig{Mounts: []string{"~/.config/coop:rw"}}
	if _, err := RunArgs("/home/user/sprocket-v2", "img", cfg, time.Minute); err == nil {
		t.Fatal("a refused mount must fail the run, not be dropped")
	}
}

func TestReaperScript(t *testing.T) {
	s := ReaperScript(30 * time.Minute)
	if !strings.Contains(s, "idle=1800") {
		t.Errorf("idle seconds missing: %s", s)
	}
	if !strings.Contains(s, "/proc/[0-9]*") {
		t.Errorf("process-count liveness signal missing: %s", s)
	}
	if !strings.Contains(s, ".coop-activity") {
		t.Errorf("activity-file liveness signal missing: %s", s)
	}
	if never := ReaperScript(0); !strings.Contains(never, "sleep infinity") {
		t.Errorf("idle 0 must never exit: %s", never)
	}
}

func TestExecArgs(t *testing.T) {
	fakeHome(t)
	repo := "/home/user/sprocket-v2"
	args := ExecArgs(repo, repo+"/scripts", "python3", []string{"x.py"}, false)
	line := argLine(args)
	if !strings.HasPrefix(line, "exec -i ") {
		t.Errorf("want non-tty exec: %s", line)
	}
	if !strings.Contains(line, "-w "+repo+"/scripts") {
		t.Errorf("cwd not passed through: %s", line)
	}
	if !strings.HasSuffix(line, ContainerName(repo)+" python3 x.py") {
		t.Errorf("command tail wrong: %s", line)
	}
	if tty := argLine(ExecArgs(repo, repo, "bash", nil, true)); !strings.Contains(tty, "-it") {
		t.Errorf("shell should get a tty: %s", tty)
	}
}

func TestResolveCwd(t *testing.T) {
	fakeHome(t)
	repo := "/home/user/sprocket-v2"
	mounts := []Mount{{Path: "/srv/data"}}
	if got := ResolveCwd(repo, repo+"/scripts", mounts); got != repo+"/scripts" {
		t.Errorf("in-repo cwd = %q", got)
	}
	if got := ResolveCwd(repo, "/srv/data/x", mounts); got != "/srv/data/x" {
		t.Errorf("mounted cwd = %q", got)
	}
	if got := ResolveCwd(repo, "/home/user/elsewhere", mounts); got != repo {
		t.Errorf("unmounted cwd = %q, want the repo root", got)
	}
}

func TestRunArgsSkipsMountsMissingOnThisHost(t *testing.T) {
	fakeHome(t)
	present := t.TempDir()
	absent := filepath.Join(t.TempDir(), "another-users-layout")
	cfg := RepoConfig{Mounts: []string{present + ":ro", absent + ":ro"}}
	args, err := RunArgs("/home/user/sprocket-v2", "img", cfg, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	line := argLine(args)
	if !strings.Contains(line, "-v "+present+":"+present+":ro") {
		t.Errorf("a mount this host does have was dropped: %s", line)
	}
	if strings.Contains(line, absent) {
		t.Errorf("docker would create %s as root and mount it empty: %s", absent, line)
	}
}
