// Package skills ships the Claude Code plugin coop injects into the
// sessions it launches — one skill, coop:init, that authors a repo's
// .coop/tools.Dockerfile.
//
// The shape deliberately mirrors hub's hook settings one hop over:
// WritePlugin materializes a tree coop owns under its state directory
// and WithPlugin appends --plugin-dir the way WithHookSettings appends
// --settings, so the plugin reaches exactly the sessions coop starts and
// nothing lands in ~/.claude. Like hooks-settings.json the tree is
// regenerated on every hub launch rather than seeded once: it is coop
// infrastructure, not a file the user is meant to edit.
//
// --plugin-dir loads a plain directory for one session — no marketplace
// entry and no install step — which is what makes injection cheap enough
// to redo at every launch.
package skills

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"coop/docs"
)

// all: is load-bearing — the plugin manifest lives in .claude-plugin,
// and a plain //go:embed drops every path element starting with a dot.
//
//go:embed all:plugin
var pluginFS embed.FS

// referencePath is where WritePlugin drops docs/toolbox.md, relative to
// the plugin root. It sits inside the skill's own directory so SKILL.md
// can name it relatively without knowing where the plugin was written.
const referencePath = "skills/init/references/toolbox.md"

// DefaultPluginDir is $XDG_STATE_HOME/coop/plugin, falling back to
// ~/.local/state (the same convention as DefaultHookSettingsPath, and
// honouring XDG for the same reason: this is written at hub launch from
// the operator's shell, not from a monitored pane whose environment is
// the judged session's to set). "" disables injection — a machine with
// no resolvable home.
func DefaultPluginDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "coop", "plugin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "coop", "plugin")
}

// WritePlugin materializes the embedded plugin at dir, replacing whatever
// is there.
//
// It builds in a sibling temp directory and renames, so a session started
// mid-write never sees a half-written plugin — a manifest with no skill
// under it is worse than no plugin at all, while the gap between the
// remove and the rename costs at most one session its /coop:init, exactly
// as -plugin=false would. Removing first (rather than merging) is what
// keeps a skill renamed across coop versions from leaving a stale second
// copy loaded.
func WritePlugin(dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".plugin-")
	if err != nil {
		return err
	}
	// No-op once the rename below has succeeded; on every failure path
	// it is what stops a partial tree accumulating next to the real one.
	defer os.RemoveAll(tmp)

	if err := copyTree(tmp); err != nil {
		return err
	}
	ref := filepath.Join(tmp, filepath.FromSlash(referencePath))
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(ref, []byte(docs.Toolbox), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// copyTree writes the embedded plugin/ subtree into root, stripping the
// "plugin/" prefix so the manifest lands at root/.claude-plugin.
func copyTree(root string) error {
	return fs.WalkDir(pluginFS, "plugin", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, "plugin"), "/")
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		raw, err := pluginFS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644)
	})
}

// WithPlugin appends the --plugin-dir and --add-dir flags to claudeCmd,
// which may carry its own flags ("claude --continue") — ours append
// after, so the user's command stays intact whatever it already says.
//
// --add-dir is not optional. A session's filesystem scope is its working
// directory, and the skill's reference file (docs/toolbox.md, the
// authority its procedure defers to) lives beside the skill under coop's
// state directory. Without the scope the skill resolves the right
// absolute path and is refused: verified, and refused for scope rather
// than for a permission rule, so an allow entry in the injected settings
// does not substitute. The cost is that a session can also write there,
// which is worth naming and not worth avoiding — this is the same user on
// the same machine (a monitored pane's $TMUX already points at coop's
// socket), and the plugin is regenerated from the binary on every hub
// launch, so anything written into it is erased rather than trusted. The
// scope is the plugin directory alone, never its parent, so the claims
// directory and the audit log stay outside it.
//
// --add-dir goes last and must stay last: it is variadic, so a flag
// appended after it is read as another directory.
func WithPlugin(claudeCmd, dir string) string {
	return claudeCmd + " --plugin-dir " + shellQuote(dir) +
		" --add-dir " + shellQuote(dir)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
