package toolbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RepoConfig is <repo>/.coop/toolbox.json: the two things a Dockerfile
// cannot state — the network the container joins and the host paths it
// can see.
type RepoConfig struct {
	Network string   `json:"network"`
	Mounts  []string `json:"mounts"`
}

// Mount is one resolved host path the container sees. There is no
// destination field on purpose: a mount appears at the same path it has
// on the host, because path identity is what lets a traceback, a config
// file's absolute path and a later Read all name the same file.
type Mount struct {
	Path string
	RW   bool
}

// Arg renders the -v value.
func (m Mount) Arg() string {
	mode := "ro"
	if m.RW {
		mode = "rw"
	}
	return m.Path + ":" + m.Path + ":" + mode
}

// RepoConfigPath is <repo>/.coop/toolbox.json.
func RepoConfigPath(repo string) string {
	return filepath.Join(repo, ".coop", "toolbox.json")
}

// LoadRepoConfig reads the repo's toolbox.json. An absent file is the
// common case and reads as a zero config; a present but unparseable one
// is an error, because guessing at what a malformed mounts list meant is
// how a repo silently loses a mount it declared.
func LoadRepoConfig(repo string) (RepoConfig, error) {
	path := RepoConfigPath(repo)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return RepoConfig{}, nil
	}
	if err != nil {
		return RepoConfig{}, err
	}
	var c RepoConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return RepoConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ParseMounts validates and resolves the declared mounts.
func ParseMounts(specs []string) ([]Mount, error) {
	var out []Mount
	for _, spec := range specs {
		m, err := parseMount(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func parseMount(spec string) (Mount, error) {
	spec = strings.TrimSpace(spec)
	path, mode := spec, "ro"
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		path, mode = spec[:i], spec[i+1:]
	}
	switch mode {
	case "ro", "rw":
	default:
		// A "dst" here is the remapping form, which the design refuses:
		// naming a different path inside would reintroduce exactly the
		// /workspace translation problem path identity removes.
		return Mount{}, fmt.Errorf("mount %q: expected \"<path>\" or \"<path>:ro\" or \"<path>:rw\"", spec)
	}
	if path != "~" && !strings.HasPrefix(path, "~/") && !filepath.IsAbs(path) {
		return Mount{}, fmt.Errorf("mount %q: path must start with / or ~/", spec)
	}
	abs := filepath.Clean(expandHome(path))
	if err := refuseCoopDirs(abs); err != nil {
		return Mount{}, fmt.Errorf("mount %q: %w", spec, err)
	}
	return Mount{Path: abs, RW: mode == "rw"}, nil
}

// refuseCoopDirs rejects a mount covering coop's own config or state
// directory. Not as a boundary — a session with code execution writes
// those directly on the host, so this stops nothing determined. It is
// the reasoning behind the arbiter's gates: it stops an unattended
// own-goal where a container rewrites arbiter.md or the claims directory
// by accident, and it costs one check.
//
// A parent counts, since mounting ~ would carry both. So does a child,
// and that is the sharper case of the two: ~/.local/state/coop/toolbox/
// <slug>/bin holds *another* repo's shims, which are #!/bin/sh scripts
// that run on the host the moment that repo's session calls python3, and
// ~/.config/coop/arbiter.md is the policy the judge obeys. Naming either
// one directly is no safer than naming the directory above it.
func refuseCoopDirs(abs string) error {
	home := userHome()
	if home == "" {
		return nil
	}
	for _, d := range []string{
		filepath.Join(home, ".config", "coop"),
		filepath.Join(home, ".local", "state", "coop"),
	} {
		if abs == d || within(d, abs) || within(abs, d) {
			return fmt.Errorf("%s is coop's own directory", d)
		}
	}
	return nil
}

// within reports whether p is inside dir.
func within(p, dir string) bool {
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home := userHome()
	if home == "" {
		return p
	}
	return filepath.Join(home, p[1:])
}
