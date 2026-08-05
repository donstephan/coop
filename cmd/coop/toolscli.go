// cmd/coop/toolscli.go
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"coop/internal/config"
	"coop/internal/toolbox"
)

// isToolsCmd matches "coop tools …". Like hook, peek and judge, it is
// dispatched before flag parsing: a shim calls it once per command, and
// it must not pay for a TUI it will never draw.
func isToolsCmd(args []string) bool {
	return len(args) > 0 && args[0] == "tools"
}

func toolsUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: coop tools <exec|ls|up|stop|prune|rebuild|shell> [args]")
}

func runToolsCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		toolsUsage(stderr)
		return 2
	}
	sub, rest := args[1], args[2:]
	switch sub {
	case "exec":
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "usage: coop tools exec <repo> <tool> [args]")
			return 2
		}
		return toolsExec(rest[0], rest[1], rest[2:])
	case "up", "stop", "rebuild", "shell":
		repo, err := toolsRepo(rest)
		if err != nil {
			fmt.Fprintln(stderr, "coop tools:", err)
			return 1
		}
		return toolsManage(sub, repo, stdout, stderr)
	case "ls":
		return toolsList(stdout, stderr)
	case "prune":
		return toolsPrune(stdout, stderr)
	default:
		toolsUsage(stderr)
		return 2
	}
}

// toolsRepo is the repo a management subcommand acts on: the argument if
// given, else the working directory.
func toolsRepo(rest []string) (string, error) {
	if len(rest) > 0 {
		return filepath.Abs(rest[0])
	}
	return os.Getwd()
}

// toolsSettings reads the toolbox settings from config.json. A missing
// file is fine (defaults); a malformed one is an error, because a
// container started with a mangled idle timeout is worse than none.
func toolsSettings() (engine toolbox.Engine, idle time.Duration, err error) {
	c, cerr := config.Load(config.DefaultPath())
	if cerr != nil && !os.IsNotExist(cerr) {
		return nil, 0, cerr
	}
	if !c.ToolboxEnabled() {
		return nil, 0, fmt.Errorf("toolbox is disabled in config.json")
	}
	idle, err = c.ToolboxIdle()
	if err != nil {
		return nil, 0, err
	}
	return toolbox.NewEngine(c.ToolboxEngine()), idle, nil
}

// toolsExec is the shim's target. The final attempt at a command is
// always a syscall.Exec of the engine binary, so exit codes and signals
// pass through exactly with no wrapper process left in the middle of
// a ^C.
func toolsExec(repo, tool string, rest []string) int {
	e, idle, err := toolsSettings()
	if err != nil {
		return execHost(tool, rest, err)
	}
	cfg, err := toolbox.LoadRepoConfig(repo)
	if err != nil {
		// The repo's own file, about the repo's own container: loud, the
		// way a container that will not start is loud. Only coop's global
		// settings fail open, because only they can break every repo.
		fmt.Fprintln(os.Stderr, "coop tools:", err)
		return 1
	}
	// Touch before the container check, so a command that starts the
	// container also counts as activity for the reaper.
	touchActivity(repo)
	_, started, err := ensureContainer(e, repo, cfg, idle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coop tools: %v (try: coop tools rebuild)\n", err)
		return 1
	}
	bin, err := e.Binary()
	if err != nil {
		fmt.Fprintln(os.Stderr, "coop tools:", err)
		return 1
	}
	mounts, _ := toolbox.ParseMounts(cfg.Mounts) // validated by ensureContainer
	cwd, _ := os.Getwd()
	args := toolbox.ExecArgs(repo, toolbox.ResolveCwd(repo, cwd, mounts), tool, rest, false)
	if !started {
		// A container that was already up can be gone again by the time
		// the engine looks — a human's docker rm -f, or the reaper's own
		// exit — so this one attempt runs as a child rather than an exec,
		// leaving a process alive to notice and retry. A container coop
		// just started needs none of that and goes straight to the exec
		// below.
		code := runEngine(bin, args)
		if code == 0 || toolbox.Running(e, repo) {
			return code
		}
		if _, _, err := ensureContainer(e, repo, cfg, idle); err != nil {
			fmt.Fprintf(os.Stderr, "coop tools: %v (try: coop tools rebuild)\n", err)
			return 1
		}
	}
	return execEngine(bin, args)
}

// ensureContainer brings the repo's container up on the image the repo
// resolves to *now*, and reports whether it had to start one.
//
// The image comparison is the point, and it is why this is not a bare
// "is it running": a container from before an overlay edit is still up
// and still holds the right name, but does not carry the tool the newly
// written shim names, so exec'ing into it fails with "executable file
// not found" for a tool the repo declared. The reconcile is nearly free
// because ImageTag hashes files rather than asking the engine — one
// inspect, which the exec path needs anyway.
func ensureContainer(e toolbox.Engine, repo string, cfg toolbox.RepoConfig,
	idle time.Duration) (image string, started bool, err error) {
	want, err := toolbox.ImageTag(repo)
	if err != nil {
		return "", false, err
	}
	st := toolbox.Inspect(e, repo)
	if st.Running && st.Image == want {
		return want, false, nil
	}
	if st.Image != "" {
		// Whatever is there holds the one name this repo's container can
		// have, and cannot serve the resolved image. Best-effort: if it
		// is already gone the run below is what matters.
		e.Run("rm", "-f", toolbox.ContainerName(repo))
	}
	if image, err = toolbox.EnsureImage(e, repo); err != nil {
		return "", false, err
	}
	if err := toolbox.Start(e, repo, image, cfg, idle); err != nil {
		return "", false, err
	}
	return image, true, nil
}

// execEngine replaces this process with the engine. Nothing after it
// runs on success.
func execEngine(bin string, args []string) int {
	argv := append([]string{filepath.Base(bin)}, args...)
	if err := syscall.Exec(bin, argv, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "coop tools:", err)
		return 1
	}
	return 0 // unreachable on success
}

// runEngine runs the engine as a child and returns the status a shell
// would report. Every fd is inherited, so stdin, output and interleaving
// are exactly what the exec would have given.
//
// SIGINT and SIGQUIT are *caught and dropped* here rather than killing
// this process: the child gets them from the terminal too, and a shell
// must not get its prompt back while the command is still dying. Caught
// rather than ignored is load-bearing — Go resets caught signals to the
// default disposition across an exec, while an inherited SIG_IGN would
// survive into the container's command and make ^C do nothing at all.
func runEngine(bin string, args []string) int {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(stop)
	go func() {
		for range stop {
		}
	}()
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, "coop tools:", err)
			return 1
		}
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}

// execHost runs the host's copy of a tool, for the case where coop's own
// settings will not resolve. This is what fail-open has to mean once
// shims exist: a shim is a file on disk and the PATH prefix is already
// baked into a live session's command, so a config.json that stopped
// parsing — or the "enabled": false the docs call the off switch — would
// otherwise hard-fail every shimmed command in every running session.
// The host's tool is what those commands ran before the toolbox existed,
// so that is what they get, silently. Only with no host tool to fall
// back to is there anything to say, and then the cause is said.
func execHost(tool string, rest []string, cause error) int {
	bin, err := toolbox.LookHost(tool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "coop tools:", cause)
		return 1
	}
	if xerr := syscall.Exec(bin, append([]string{tool}, rest...), os.Environ()); xerr != nil {
		fmt.Fprintln(os.Stderr, "coop tools:", cause)
		return 1
	}
	return 0 // unreachable on success
}

// touchActivity updates the file the in-container reaper reads as its
// second liveness signal. Best-effort: a miss costs at most one early
// idle-out, which the next command self-heals.
func touchActivity(repo string) {
	path := toolbox.ActivityFile(repo)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	if f, err := os.Create(path); err == nil {
		f.Close()
	}
}

func toolsManage(sub, repo string, stdout, stderr io.Writer) int {
	e, idle, err := toolsSettings()
	if err != nil {
		fmt.Fprintln(stderr, "coop tools:", err)
		return 1
	}
	cfg, err := toolbox.LoadRepoConfig(repo)
	if err != nil {
		fmt.Fprintln(stderr, "coop tools:", err)
		return 1
	}
	switch sub {
	case "stop":
		if err := e.Run("rm", "-f", toolbox.ContainerName(repo)); err != nil {
			fmt.Fprintln(stderr, "coop tools:", err)
			return 1
		}
		fmt.Fprintln(stdout, "stopped", toolbox.ContainerName(repo))
		return 0
	case "rebuild":
		// A rebuild must not reuse the old image, and a container on it
		// has to go first.
		e.Run("rm", "-f", toolbox.ContainerName(repo)) // best-effort
		if err := toolsRebuild(e, repo, stdout); err != nil {
			fmt.Fprintln(stderr, "coop tools:", err)
			return 1
		}
		return 0
	case "up", "shell":
		image, _, err := ensureContainer(e, repo, cfg, idle)
		if err != nil {
			fmt.Fprintln(stderr, "coop tools:", err)
			return 1
		}
		if sub == "up" {
			fmt.Fprintln(stdout, toolbox.ContainerName(repo), "running", image)
			return 0
		}
		bin, err := e.Binary()
		if err != nil {
			fmt.Fprintln(stderr, "coop tools:", err)
			return 1
		}
		return execEngine(bin, toolbox.ExecArgs(repo, repo, "bash", nil, true))
	}
	return 0
}

// newToolboxCmd returns the function that puts a new session's shim
// directory on its PATH, or nil when the toolbox is off or unusable.
// Failures here are fail-open by design: no shim directory, an untouched
// PATH, and coop behaves exactly as it did before the toolbox existed.
func newToolboxCmd(cfgPath string) (func(dir, cmd string) string, error) {
	c, err := config.Load(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if !c.ToolboxEnabled() {
		return nil, nil
	}
	// Validated here, where the answer is still "write no shims", rather
	// than left to the first exec. Shims persist on disk and the PATH
	// prefix is already in a live session's command, so a value that only
	// failed at exec time would be a bad line in config.json hard-failing
	// every shimmed command in every running session.
	if _, err := c.ToolboxIdle(); err != nil {
		return nil, err
	}
	e := toolbox.NewEngine(c.ToolboxEngine())
	if _, err := e.Binary(); err != nil {
		return nil, nil // no engine installed: not an error, just off
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return func(dir, cmd string) string {
		go func() {
			// Best-effort: a failed prepare leaves the shim directory
			// empty and the session on host tools.
			if perr := toolbox.Prepare(e, dir, exe); perr != nil {
				appendBuildLog(dir, perr)
			}
		}()
		return toolbox.WithToolbox(cmd, toolbox.ShimDir(dir))
	}, nil
}

// appendBuildLog records a failed prepare. The TUI has no room for a
// build error and the goroutine has no terminal, so this file is the
// only channel it has — the same reasoning as judge.log.
func appendBuildLog(repo string, cause error) {
	root := toolbox.StateRoot()
	if root == "" {
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(root, "build.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s: %v\n", time.Now().Format(time.RFC3339), repo, cause)
}

// toolsRebuild rebuilds the repo's image with build output on the
// terminal — the one place a human is watching, and the reason the
// asynchronous path logs to a file instead.
func toolsRebuild(e toolbox.Engine, repo string, stdout io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := toolbox.Prepare(e, repo, exe); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "rebuilt", repo)
	return nil
}

func toolsList(stdout, stderr io.Writer) int {
	e, _, err := toolsSettings()
	if err != nil {
		fmt.Fprintln(stderr, "coop tools:", err)
		return 1
	}
	out, err := e.Output("ps", "--filter", "label=coop.managed=true",
		"--format", "{{.Names}}\t{{.Image}}\t{{.Status}}")
	if err != nil {
		fmt.Fprintln(stderr, "coop tools:", err)
		return 1
	}
	if strings.TrimSpace(out) == "" {
		fmt.Fprintln(stdout, "no toolbox containers running")
		return 0
	}
	fmt.Fprint(stdout, out)
	return 0
}

// toolsPrune removes coop's stopped containers and its superseded
// images. The current base is spared by name — pruning the image the
// next session needs would only mean rebuilding it.
func toolsPrune(stdout, stderr io.Writer) int {
	e, _, err := toolsSettings()
	if err != nil {
		fmt.Fprintln(stderr, "coop tools:", err)
		return 1
	}
	e.Run("container", "prune", "-f", "--filter", "label=coop.managed=true")
	out, err := e.Output("images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		fmt.Fprintln(stderr, "coop tools:", err)
		return 1
	}
	keep := map[string]bool{toolbox.BaseTag(): true, toolbox.BaseMovingTag: true}
	for _, tag := range strings.Fields(out) {
		if !strings.HasPrefix(tag, "coop-tools") || keep[tag] {
			continue
		}
		if err := e.Run("rmi", tag); err == nil {
			fmt.Fprintln(stdout, "removed", tag)
		}
	}
	return 0
}
