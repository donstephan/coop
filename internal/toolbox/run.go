// internal/toolbox/run.go
package toolbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// envPassThrough is the only environment a container inherits from the
// session. Everything else is deliberately dropped: the image's own ENV
// is authoritative, so repo-specific environment belongs in the overlay,
// where it is declared once and travels with the repo. What is left
// through only decides how text renders.
var envPassThrough = []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "TZ"}

// ReaperScript is the container's PID 1. It exits when nothing has run
// for idle, which is what makes the container reap itself with no hub,
// no refcount and nothing for two hubs to race on.
//
// Two liveness signals, because either alone has a hole. A process
// besides PID 1 catches a long command directly, but samples once a
// minute and so can miss a series of short ones; the activity file,
// touched by coop tools exec before every command, catches those. bash
// rather than sh so $EPOCHSECONDS and the glob count are builtins — a
// reaper that forked to measure its own idleness would never be idle.
func ReaperScript(idle time.Duration) string {
	if idle <= 0 {
		return "exec sleep infinity"
	}
	secs := strconv.Itoa(int(idle.Seconds()))
	return `set -u
idle=` + secs + `
activity=$HOME/.coop-activity
last=$EPOCHSECONDS
while :; do
  sleep 60
  pids=(/proc/[0-9]*)
  if (( ${#pids[@]} > 1 )); then
    last=$EPOCHSECONDS
    continue
  fi
  if [ -e "$activity" ]; then
    seen=$(date -r "$activity" +%s 2>/dev/null || echo 0)
    if (( seen > last )); then
      last=$seen
      continue
    fi
  fi
  (( EPOCHSECONDS - last >= idle )) && exit 0
done`
}

// RunArgs is the full argv creating one repo's container. It is built
// here, apart from the spawn, so every restriction on it can be asserted
// without a container engine.
func RunArgs(repo, image string, cfg RepoConfig, idle time.Duration) ([]string, error) {
	home := HomeDir(repo)
	if home == "" {
		return nil, fmt.Errorf("no resolvable home directory")
	}
	mounts, err := ParseMounts(cfg.Mounts)
	if err != nil {
		return nil, err
	}
	// A declared path this host does not have is not a mount. The
	// engine would create it as root and mount it empty; the human-
	// facing "coop tools" subcommands report what was skipped.
	mounts, _ = SplitMounts(mounts)
	args := []string{"run", "-d", "--rm",
		"--name", ContainerName(repo),
		"--label", "coop.managed=true",
		"--label", "coop.repo=" + repo,
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		// The repo at its own host path: a traceback, a config file's
		// absolute path and anything claude then hands to Read all name
		// the same file, with no translation layer to get wrong.
		"-v", repo + ":" + repo + ":rw",
		"-v", "/tmp:/tmp:rw",
		"-v", home + ":" + home + ":rw",
		"-e", "HOME=" + home,
		"-w", repo,
	}
	// Caches resolve under the persisted home, never the repo tree. This
	// is load-bearing rather than hygiene once a repo is all-in: without
	// it every idle-out means a cold rebuild, and the default location
	// for environment-coupled state would be the working tree.
	// A slice, not a map: a map's iteration order would make the argv
	// non-deterministic, and an argv that varies run to run is a poor
	// thing to debug from "docker inspect".
	for _, kv := range [][2]string{
		{"GOCACHE", filepath.Join(home, ".cache", "go-build")},
		{"PIP_CACHE_DIR", filepath.Join(home, ".cache", "pip")},
		{"npm_config_cache", filepath.Join(home, ".cache", "npm")},
		{"CARGO_HOME", filepath.Join(home, ".cargo")},
	} {
		args = append(args, "-e", kv[0]+"="+kv[1])
	}
	for _, name := range envPassThrough {
		if v, ok := os.LookupEnv(name); ok {
			args = append(args, "-e", name+"="+v)
		}
	}
	for _, m := range mounts {
		args = append(args, "-v", m.Arg())
	}
	if cfg.Network != "" {
		args = append(args, "--network", cfg.Network)
	}
	// The docker socket is never mounted. Mounting it would turn any
	// shimmed tool into host root — the one mistake in this design space
	// that is not recoverable.
	return append(args, image, "bash", "-c", ReaperScript(idle)), nil
}

// ExecArgs is one command's argv. -i for stdin; -t only for coop tools
// shell, since the Bash tool has no tty and -t would fail there.
func ExecArgs(repo, cwd, tool string, args []string, tty bool) []string {
	flag := "-i"
	if tty {
		flag = "-it"
	}
	out := []string{"exec", flag, "-w", cwd, ContainerName(repo), tool}
	return append(out, args...)
}

// ResolveCwd picks the working directory for an exec. A cwd the container
// cannot see would fail the exec outright, so anything outside the repo
// and its declared mounts falls back to the repo root — a command run
// from an unmounted directory should work on the repo, not error.
func ResolveCwd(repo, cwd string, mounts []Mount) string {
	if cwd == "" {
		return repo
	}
	visible := []string{repo, "/tmp", HomeDir(repo)}
	for _, m := range mounts {
		visible = append(visible, m.Path)
	}
	for _, v := range visible {
		if v == "" {
			continue
		}
		if cwd == v || strings.HasPrefix(cwd, v+string(filepath.Separator)) {
			return cwd
		}
	}
	return repo
}

// ContainerState is what one inspect says about a repo's container:
// whether it is up, and the image it was created from. Both come from a
// single call because a caller needs them together — a container running
// the image before an overlay edit is as useless as no container at all,
// since the tool the new shim was written for is not in it.
type ContainerState struct {
	Running bool
	Image   string
}

// Inspect reads the repo's container state. A container that is not
// there reads as the zero value, which is also what an engine error
// reads as: both mean "nothing usable is running", and the caller's
// answer to either is to create one.
func Inspect(e Engine, repo string) ContainerState {
	out, err := e.Output("inspect", "-f", "{{.State.Running}} {{.Config.Image}}", ContainerName(repo))
	if err != nil {
		return ContainerState{}
	}
	fields := strings.Fields(strings.TrimSpace(out))
	var st ContainerState
	if len(fields) > 0 {
		st.Running = fields[0] == "true"
	}
	if len(fields) > 1 {
		st.Image = fields[1]
	}
	return st
}

// Running reports whether the repo's container is up, whatever image it
// is on. Callers that are about to run a command want Inspect instead.
func Running(e Engine, repo string) bool { return Inspect(e, repo).Running }

// Start brings up the repo's container. The home directory is created
// first: docker would otherwise create it as root, leaving the container
// unable to write its own $HOME.
func Start(e Engine, repo, image string, cfg RepoConfig, idle time.Duration) error {
	if home := HomeDir(repo); home != "" {
		if err := os.MkdirAll(home, 0o755); err != nil {
			return err
		}
		// Best-effort: the marker is documentation, and failing to write
		// it must not stop a container coming up.
		_ = WriteHomeMarker(repo)
	}
	args, err := RunArgs(repo, image, cfg, idle)
	if err != nil {
		return err
	}
	return e.Run(args...)
}
