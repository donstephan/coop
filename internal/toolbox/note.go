package toolbox

import (
	"os"
	"path/filepath"
	"strings"
)

// ShimDirOn picks coop's shim directory out of a PATH, "" if there is
// none. It is the only honest way for a process running *inside* a
// monitored session to learn whether that session is shimmed: the repo
// root is not recoverable from the payload (a hook's cwd is wherever the
// session happens to be, which may be well below the repo), and the slug
// is hashed, so there is nothing to reverse. The PATH entry is the fact
// itself — a shim directory being on it is precisely what "this session's
// commands go to a container" means.
//
// Pure string work, no I/O: the directory it names may not exist yet,
// since Prepare writes shims asynchronously.
func ShimDirOn(path string) string {
	root := StateRoot()
	if root == "" {
		return ""
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return dir
		}
	}
	return ""
}

// SessionNote is what coop tells a session about its own toolbox, for
// injection as SessionStart additionalContext. "" when there is nothing
// to say, which is the common case and must stay silent.
//
// It is derived from the shims **on disk**, exactly as Grants is, and for
// the same reason: Prepare builds asynchronously, so a first session on a
// fresh clone starts before any shim exists, and a build can fail
// outright. A note listing tools that are not shimmed would be telling
// the session its commands are containerized at the moment they are not —
// which is worse than saying nothing, because the whole value here is
// that the session can trust it.
//
// The content is deliberately facts and one prohibition, not rules. What
// a repo may declare, the reserved names, all-in/all-out and the build
// context live in docs/toolbox.md and in the /coop:init skill, and a
// third copy here would be the one that goes stale — the same deferral
// SKILL.md already makes. What cannot be discovered from inside the
// session, and so has to be said, is the list, the host-path identity,
// and the $HOME that swallows an install.
func SessionNote(shimDir string) string {
	tools := shimNames(shimDir)
	if len(tools) == 0 {
		return ""
	}
	home := filepath.Join(filepath.Dir(shimDir), "home")
	return "coop toolbox: in this session these commands run in a container " +
		"for this repo, not on the host.\n\n" +
		"    " + strings.Join(tools, " ") + "\n\n" +
		"They are shims on PATH, so you invoke them by their bare names as " +
		"usual and nothing about the command string changes. The repo is " +
		"bind-mounted at its own host path and the container runs as your " +
		"uid, so a path means the same thing inside and out.\n\n" +
		"Do not route around a shim with an absolute path (/usr/bin/go): " +
		"that runs the host's copy and splits the toolchain the container " +
		"exists to keep whole. If a shimmed command fails because the tool " +
		"is missing from the image, the fix is the repo's " +
		".coop/tools.Dockerfile, never the host.\n\n" +
		"$HOME in the container is " + home + ", not your own home. What " +
		"`go install`, `pip install --user`, `npm i -g` and `cargo " +
		"install` write lands there and is invisible from the host; " +
		"`coop tools home` prints the path. Run /coop:init to change what " +
		"this repo declares.\n"
}

// shimNames lists the shims actually written for a repo. Directory
// entries only — WriteShims writes plain files, so anything else in there
// is not a shim and would not be executed as one either.
func shimNames(dir string) []string {
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir) // already sorted by name
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		out = append(out, ent.Name())
	}
	return out
}
