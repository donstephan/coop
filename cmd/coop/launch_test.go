package main

import (
	"strings"
	"testing"
)

// --add-dir is variadic, so anything appended after it is read as another
// directory rather than as a flag. That is the constraint that forces the
// whole command to be assembled per session, and it fails silently: the
// session starts, --settings is swallowed as a path, and the status tier
// and every grant vanish with nothing logged.
func TestLauncherKeepsAddDirLast(t *testing.T) {
	l := launcher{
		hooks:          true,
		exe:            "/home/user/bin/coop",
		globalSettings: "/state/coop/hooks-settings.json",
		pluginDir:      "/state/coop/plugin",
		toolboxCmd:     func(_, cmd string) string { return "env PATH='/shims':\"$PATH\" " + cmd },
		settingsFor:    func(_, _ string) string { return "/state/coop/toolbox/sprocket-v2-a1b2c3/settings.json" },
	}
	got := l.command("/home/user/sprocket-v2", "claude")

	settings, plugin := strings.Index(got, "--settings"), strings.Index(got, "--plugin-dir")
	addDir := strings.Index(got, "--add-dir")
	if settings < 0 || plugin < 0 || addDir < 0 {
		t.Fatalf("missing a flag: %q", got)
	}
	if rest := got[addDir+len("--add-dir"):]; strings.Contains(rest, " --") {
		t.Errorf("a flag follows --add-dir: %q", got)
	}
	if !(settings < plugin && plugin < addDir) {
		t.Errorf("flag order is settings=%d plugin=%d add-dir=%d in %q", settings, plugin, addDir, got)
	}
}

// The per-repo file is what carries that repo's grants; the global one
// has hooks and nothing else. Falling back to the global file when the
// per-repo write fails costs the session its grants, never its hooks.
func TestLauncherPrefersPerRepoSettings(t *testing.T) {
	base := launcher{
		hooks:          true,
		exe:            "/home/user/bin/coop",
		globalSettings: "/state/coop/hooks-settings.json",
		toolboxCmd:     func(_, cmd string) string { return cmd },
	}

	perRepo := base
	perRepo.settingsFor = func(_, _ string) string { return "/state/coop/toolbox/sprocket-v2-a1b2c3/settings.json" }
	if got := perRepo.command("/home/user/sprocket-v2", "claude"); !strings.Contains(got, "toolbox/sprocket-v2-a1b2c3/settings.json") {
		t.Errorf("did not use the per-repo settings: %q", got)
	}

	failed := base
	failed.settingsFor = func(_, _ string) string { return "" }
	if got := failed.command("/home/user/sprocket-v2", "claude"); !strings.Contains(got, "hooks-settings.json") {
		t.Errorf("did not fall back to the global settings: %q", got)
	}
}

// Shims outlive a config change, so a toolbox turned off can leave a full
// shim directory on disk. Deriving grants from it while nothing puts it
// on PATH is exactly the unbacked grant this design removes.
func TestLauncherWritesNoGrantsWithoutTheToolbox(t *testing.T) {
	called := false
	l := launcher{
		hooks:          true,
		exe:            "/home/user/bin/coop",
		globalSettings: "/state/coop/hooks-settings.json",
		toolboxCmd:     nil, // toolbox off, or no engine installed
		settingsFor: func(_, _ string) string {
			called = true
			return "/state/coop/toolbox/sprocket-v2-a1b2c3/settings.json"
		},
	}
	got := l.command("/home/user/sprocket-v2", "claude")
	if called {
		t.Error("derived per-repo grants with the toolbox off")
	}
	if !strings.Contains(got, "hooks-settings.json") {
		t.Errorf("lost the hook settings: %q", got)
	}
}

// -hooks=false means no --settings at all: e2e-smoke.sh's fake claude
// takes no flags, and a settings file carrying only permissions would
// still be a flag it cannot parse.
func TestLauncherHooksOffAppendsNoSettings(t *testing.T) {
	l := launcher{
		hooks:          false,
		globalSettings: "/state/coop/hooks-settings.json",
		toolboxCmd:     func(_, cmd string) string { return cmd },
		settingsFor:    func(_, _ string) string { return "/state/coop/toolbox/x/settings.json" },
	}
	if got := l.command("/home/user/sprocket-v2", "sleep 300"); strings.Contains(got, "--settings") {
		t.Errorf("appended --settings with hooks off: %q", got)
	}
}
