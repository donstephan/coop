// Package toolbox runs one container per repo holding the command-line
// tooling that repo declares, and generates the PATH shims that route a
// session's commands into it. See the toolbox section of CLAUDE.md for
// why it is shaped this way, and docs/toolbox.md for how to use it.
//
// Nothing here is a security boundary: the repo is bind-mounted
// read-write at its host path and the container runs as the invoking
// user. It is a reproducible toolchain, not a sandbox.
package toolbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// userHome resolves the home directory from the password database, not
// $HOME, and never from $XDG_STATE_HOME. coop tools exec runs inside the
// monitored pane, where both of those are the session's to set — the same
// reasoning as claimRoot in internal/hub/judgeexec.go. Indirected through
// a var so tests can point the state tree at a t.TempDir(); a test cannot
// redirect it with an environment variable, because not being redirectable
// by one is the property this has.
var userHome = passwdHome

func passwdHome() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// Slug renders a repo path as a token legal as both a docker container
// name and a path element. The basename leads so a human reading
// "docker ps" can tell which repo a container belongs to; the path hash
// follows so two checkouts of the same project do not share a container.
func Slug(repo string) string {
	clean := filepath.Clean(repo)
	sum := sha256.Sum256([]byte(clean))
	base := strings.ToLower(filepath.Base(clean))
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, base)
	base = strings.Trim(base, "-")
	if base == "" {
		base = "repo"
	}
	return base + "-" + hex.EncodeToString(sum[:4])
}

// StateRoot is ~/.local/state/coop/toolbox ("" if home is unknown).
func StateRoot() string {
	home := userHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "coop", "toolbox")
}

func repoRoot(repo string) string {
	root := StateRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, Slug(repo))
}

// ShimDir holds the generated shims for one repo. It is prepended to the
// session's PATH.
func ShimDir(repo string) string {
	if r := repoRoot(repo); r != "" {
		return filepath.Join(r, "bin")
	}
	return ""
}

// HomeDir is the container's $HOME, mounted and persistent so package
// caches survive an idle-out. It is not the user's home: anything
// installed into it is invisible from the host. See the spec.
func HomeDir(repo string) string {
	if r := repoRoot(repo); r != "" {
		return filepath.Join(r, "home")
	}
	return ""
}

// ActivityFile is touched by coop tools exec before every command and
// read by the in-container reaper. The reaper's other liveness signal —
// a process besides PID 1 — samples once a minute and so can miss a
// series of short commands; this closes that hole. It lives under
// HomeDir because HomeDir is already mounted.
func ActivityFile(repo string) string {
	if h := HomeDir(repo); h != "" {
		return filepath.Join(h, ".coop-activity")
	}
	return ""
}

// ContainerName is the long-lived container for one repo, shared by
// every session on it.
func ContainerName(repo string) string { return "coop-tools-" + Slug(repo) }
