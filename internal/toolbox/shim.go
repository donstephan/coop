// internal/toolbox/shim.go
package toolbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestPath is where an image declares the commands coop shims.
const ManifestPath = "/etc/coop/tools"

// reservedTools are never shimmed, whatever an image's manifest says.
// They are the binaries coop itself shells out to by bare name from
// inside a monitored pane, and a shim for one of them does not fail
// loudly, it silently reroutes coop's own plumbing:
//
//   - tmux. internal/hub runs "tmux" through PATH, and coop hook runs in
//     the monitored pane where the shim directory is first. The
//     container's tmux would then talk to the host's server across a
//     protocol version it does not speak — and because coop hook is
//     silent and always exits 0, the whole hook status tier and the
//     arbiter would degrade to title-only with nothing logged anywhere.
//   - docker/podman. coop tools exec inherits the same shimmed PATH and
//     resolves the engine through it, so a shimmed engine means the shim
//     calls coop tools exec, which finds the shim again, until the
//     process limit.
//   - coop. Same recursion, one hop shorter.
//
// Keeping the tool *installed* in an image is fine and often useful (a
// pinned tmux is how coop's own suite runs in the container); it is
// putting it on the session's PATH that breaks.
var reservedTools = map[string]bool{
	"tmux":   true,
	"docker": true,
	"podman": true,
	"coop":   true,
}

// ParseManifest reads an image's manifest. Blank lines and # comments are
// dropped, and so is anything that is not a plain command token: the
// manifest comes from an image a repo authored, and a name carrying a
// slash or a shell metacharacter would either write a shim outside the
// shim directory or inject into the shim body. Reserved names go the same
// way. Silently dropping is right here — a bad line costs one missing
// shim and the host's tool, where refusing the whole manifest would cost
// every shim.
func ParseManifest(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !safeTool(line) {
			continue
		}
		if reservedTools[line] {
			continue
		}
		out = append(out, line)
	}
	return out
}

func safeTool(s string) bool {
	if s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '+':
		default:
			return false
		}
	}
	return true
}

// ReadManifest reads the manifest out of an image without starting the
// long-lived container. It has to be a one-shot: nothing starts that
// container until a shim is called, and no shim exists until this has
// run. --entrypoint cat overrides whatever the image would otherwise do.
func ReadManifest(e Engine, image string) ([]string, error) {
	out, err := e.Output("run", "--rm", "--entrypoint", "cat", image, ManifestPath)
	if err != nil {
		return nil, err
	}
	return ParseManifest(out), nil
}

// RenderShim is one shim: a two-line stub deferring everything to
// coop tools exec, which holds the resolve/start/retry logic in Go where
// it can be tested. The shim itself never changes, so a coop upgrade
// does not have to rewrite behaviour into hundreds of scripts.
func RenderShim(exe, repo, tool string) string {
	return "#!/bin/sh\nexec " + shellQuote(exe) + " tools exec " +
		shellQuote(repo) + " " + shellQuote(tool) + " \"$@\"\n"
}

// WriteShims makes dir hold exactly one executable shim per tool.
// Stale shims are removed: a tool dropped from a repo's manifest must
// stop shadowing the host's copy of it.
func WriteShims(dir, exe, repo string, tools []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, tool := range tools {
		want[tool] = true
		path := filepath.Join(dir, tool)
		if err := os.WriteFile(path, []byte(RenderShim(exe, repo, tool)), 0o755); err != nil {
			return err
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, ent := range ents {
		if !want[ent.Name()] {
			os.Remove(filepath.Join(dir, ent.Name())) // best-effort
		}
	}
	return nil
}

// WithToolbox prefixes the session's command with the shim directory on
// PATH. $PATH stays unquoted-by-us and expanded by the shell tmux runs
// the command under, so the host's own PATH follows the shims.
func WithToolbox(claudeCmd, shimDir string) string {
	if shimDir == "" {
		return claudeCmd
	}
	return "env PATH=" + shellQuote(shimDir) + `:"$PATH" ` + claudeCmd
}

// HostPath is PATH with coop's shim directories — everything under
// StateRoot — removed. Falling back to the host's copy of a tool has to
// resolve it on this rather than on the session's own PATH, where the
// shim directory is first and the lookup would find the shim that called
// coop in the first place.
func HostPath(path string) string {
	root := StateRoot()
	var keep []string
	for _, dir := range filepath.SplitList(path) {
		if root != "" && (dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))) {
			continue
		}
		keep = append(keep, dir)
	}
	return strings.Join(keep, string(filepath.ListSeparator))
}

// LookHost resolves a tool on HostPath. It is deliberately not
// exec.LookPath: that reads $PATH, which is the shimmed one.
func LookHost(tool string) (string, error) {
	for _, dir := range filepath.SplitList(HostPath(os.Getenv("PATH"))) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, tool)
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: not found outside coop's shim directories", tool)
}

// repoTools decides which commands this repo shims, from the two places
// that can say so.
//
// A "commands" block in .coop/toolbox.json is the **complete** list for
// that repo: it does not extend the image's manifest, it replaces it, so
// a declared repo is one file you can read to know everything that is
// shimmed. A repo that declares commands and omits python3 does not get
// python3 from the base image — that is the cost of explicit, and it is
// deliberate rather than an oversight.
//
// With no block at all the image's own manifest is the whole story, which
// is the zero-config path: a repo with no .coop/ still gets the base
// image's tools, exactly as before this field existed.
func repoTools(e Engine, repo, image string) ([]string, error) {
	cfg, err := LoadRepoConfig(repo)
	if err != nil {
		return nil, err
	}
	if names, declared := cfg.CommandNames(); declared {
		return names, nil
	}
	return ReadManifest(e, image)
}

// MissingTools reports which of tools the image cannot resolve.
//
// This is the check that replaces co-location. While the manifest lived
// in the Dockerfile it sat directly under the RUN that installed the
// binary, so declaring without installing took effort; a "commands" block
// in toolbox.json is a second file, and the two can now disagree
// silently. The symptom is bad — the shim exists, so the guard lets the
// command through, and it fails at exec with docker's bare "executable
// file not found" naming a tool the repo thought it had declared.
//
// One container run for the whole list, not one per tool. The names have
// already been through safeTool, so they cannot carry a metacharacter
// into this script.
//
// A check that cannot run reports nothing rather than failing: this is a
// diagnostic, and an engine hiccup here must not stop a session being
// created.
func MissingTools(e Engine, image string, tools []string) []string {
	if len(tools) == 0 {
		return nil
	}
	script := "for t in " + strings.Join(tools, " ") +
		`; do command -v "$t" >/dev/null 2>&1 || echo "$t"; done`
	out, err := e.Output("run", "--rm", "--entrypoint", "sh", image, "-c", script)
	if err != nil {
		return nil
	}
	var missing []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			missing = append(missing, line)
		}
	}
	return missing
}

// Prepare resolves the repo's image and writes its shims. This runs at
// session create, not at container start: shims cannot be generated from
// a running container, because nothing starts the container until a shim
// is called.
//
// The returned names are declared commands the image cannot resolve.
// They are a warning, not an error: the shims are written anyway, because
// the alternative — silently skipping one — turns a typo into "that tool
// quietly uses the host copy", which is the failure the whole declaration
// exists to prevent. Failing loud at exec is the toolbox's existing stance
// for anything about the container.
func Prepare(e Engine, repo, exe string) ([]string, error) {
	image, err := EnsureImage(e, repo)
	if err != nil {
		return nil, err
	}
	tools, err := repoTools(e, repo, image)
	if err != nil {
		return nil, err
	}
	dir := ShimDir(repo)
	if dir == "" {
		return nil, nil // no resolvable home: fail open, host tools stay in play
	}
	if err := WriteShims(dir, exe, repo, tools); err != nil {
		return nil, err
	}
	return MissingTools(e, image, tools), nil
}

// shellQuote wraps s in single quotes, escaping any it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
