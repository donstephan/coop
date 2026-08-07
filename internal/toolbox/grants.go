package toolbox

import (
	"os"
	"path/filepath"
)

// SettingsPath is the Claude Code settings file coop injects with
// --settings into sessions on this repo. It sits beside the repo's shims
// rather than in one global file because what it carries is per-repo: the
// Bash(<tool> *) grants derived from that repo's declared commands.
//
// Living under coop's state directory rather than in the repo is the
// whole point. A committed .claude/settings.json also reaches a claude
// somebody starts outside coop, where no shim is on PATH and the same
// rule means "run the host's copy, unattended, no prompt" — a grant made
// under an assumption that silently stopped holding. Injected settings
// reach exactly the sessions coop launched, which are exactly the
// sessions that have the shims the grant assumes.
func SettingsPath(repo string) string {
	if r := repoRoot(repo); r != "" {
		return filepath.Join(r, "settings.json")
	}
	return ""
}

// Grants returns the commands a session on repo may run without a
// permission prompt: the repo's declared "allow": true set, intersected
// with the shims that actually exist on disk right now.
//
// The intersection is load-bearing, and it is what replaces the old
// PreToolUse guard. A grant is only safe while the bare name resolves to
// a shim, and Prepare writes shims asynchronously — a first session on a
// fresh clone launches minutes before the image finishes building, and a
// build can fail outright. Deriving from the declaration alone would hand
// those sessions Bash(go *) with no shim behind it, which is the host's
// go running unattended. Deriving from the directory means the grant
// cannot outrun the tool it grants: no shim, no rule, automatically, with
// nothing to re-check at call time.
//
// It is a stat per declared command against a directory coop wrote, so it
// is cheap enough to run synchronously on the session-create path.
//
// Failure is silence rather than an error. Every caller's fallback is
// "write no grants", which is the safe direction and the behaviour of
// every coop before this existed.
func Grants(repo string) []string {
	dir := ShimDir(repo)
	if dir == "" {
		return nil
	}
	cfg, err := LoadRepoConfig(repo)
	if err != nil {
		return nil
	}
	names, _ := cfg.CommandNames()
	var out []string
	for _, name := range names {
		// Not IsDir: a directory named python3 is not a shim, and
		// exec would not treat it as one either.
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		// Every rule for this command, so a narrowed grant is filtered
		// by the same shim its whole-command form would have been.
		out = append(out, cfg.Commands[name].Rules(name)...)
	}
	return out
}
