// cmd/coop/launch.go
package main

import (
	"coop/internal/hub"
	"coop/internal/skills"
)

// launcher assembles the command one session is launched with.
//
// It is assembled per session rather than once at hub launch because the
// --settings file is per repo: it carries that repo's Bash(<tool> *)
// grants, derived from the shims that exist for it. Before grants there
// was nothing repo-dependent to append, and the whole command could be
// built once.
type launcher struct {
	// hooks is the -hooks switch, already ANDed with "we know our own
	// path" — without that there are no hooks to register and no shims to
	// name, so there is no settings file to write at all. Off means no
	// --settings flag whatsoever, which is what keeps e2e-smoke.sh's fake
	// claude (which takes no flags) launchable.
	hooks bool
	exe   string
	// globalSettings is the hook-only file, used for a session whose repo
	// has no toolbox to derive grants from. "" if it could not be written.
	globalSettings string
	// pluginDir is the coop plugin carrying /coop:init. "" if off.
	pluginDir string
	// toolboxCmd prepends the repo's shim directory to PATH and kicks off
	// the background image build. nil when the toolbox is off or no
	// engine is installed, which is also what says grants must not be
	// derived: shims outlive a config change, so a stale shim directory
	// with nothing putting it on PATH would yield a grant with no shim
	// behind it.
	toolboxCmd func(dir, cmd string) string
	// settingsFor writes the per-repo settings file and returns its path,
	// "" to fall back to globalSettings. Indirected for tests.
	settingsFor func(repo, exe string) string
}

// command wraps base for a session on repo dir.
//
// The order is forced, not stylistic. --settings first, then the plugin,
// because skills.WithPlugin ends in --add-dir, which is variadic: a flag
// appended after it is read as another directory. The toolbox goes last
// only because it prefixes (env PATH=…) rather than appends, so it cannot
// disturb the flags either way.
func (l launcher) command(dir, base string) string {
	cmd := base
	if l.hooks {
		settings := l.globalSettings
		if l.toolboxCmd != nil && l.settingsFor != nil {
			if p := l.settingsFor(dir, l.exe); p != "" {
				settings = p
			}
		}
		if settings != "" {
			cmd = hub.WithHookSettings(cmd, settings)
		}
	}
	if l.pluginDir != "" {
		cmd = skills.WithPlugin(cmd, l.pluginDir) // --add-dir must stay last
	}
	if l.toolboxCmd != nil {
		cmd = l.toolboxCmd(dir, cmd)
	}
	return cmd
}
