package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noteFixture points the state tree at a temp home and writes shims for
// repo, returning the repo path and its shim directory.
func noteFixture(t *testing.T, shims ...string) (string, string) {
	t.Helper()
	home := t.TempDir()
	old := userHome
	userHome = func() string { return home }
	t.Cleanup(func() { userHome = old })

	repo := filepath.Join(t.TempDir(), "sprocket-v2")
	dir := ShimDir(repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range shims {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo, dir
}

// The shim directory is found by its place under the state tree, whether
// it is first on PATH (where a session puts it) or anywhere else.
func TestShimDirOnFindsTheShimDir(t *testing.T) {
	_, dir := noteFixture(t, "go")
	for _, path := range []string{
		dir + ":/usr/bin:/bin",
		"/usr/bin:" + dir + ":/bin",
		dir,
	} {
		if got := ShimDirOn(path); got != dir {
			t.Errorf("ShimDirOn(%q) = %q, want %q", path, got, dir)
		}
	}
}

// An unshimmed session's PATH names no such directory, and neither does
// one that merely resembles the state root.
func TestShimDirOnIgnoresEverythingElse(t *testing.T) {
	_, dir := noteFixture(t, "go")
	root := StateRoot()
	for _, path := range []string{
		"",
		"/usr/bin:/bin",
		// A sibling whose name only shares the root's prefix: a session
		// on a repo called ".../coop/toolboxes" is not shimmed by it.
		root + "-elsewhere/bin",
	} {
		if got := ShimDirOn(path); got != "" {
			t.Errorf("ShimDirOn(%q) = %q, want empty", path, got)
		}
	}
	// Sanity: the fixture's own directory is still found, so the cases
	// above failed for the right reason.
	if ShimDirOn(dir) == "" {
		t.Error("fixture shim dir not found")
	}
}

// The note names the tools that are shimmed and the container HOME they
// install into — the two things a session cannot discover for itself.
func TestSessionNote(t *testing.T) {
	repo, dir := noteFixture(t, "go", "python3")
	note := SessionNote(dir)
	for _, want := range []string{
		"go python3",
		HomeDir(repo),
		"coop tools home",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note is missing %q:\n%s", want, note)
		}
	}
}

// No shims, no note. Prepare builds asynchronously, so a first session on
// a fresh clone starts before the directory has anything in it — and
// telling that session its commands are containerized when they are not
// is worse than telling it nothing, since the note's whole value is that
// it can be trusted.
func TestSessionNoteSilentWithoutShims(t *testing.T) {
	_, dir := noteFixture(t)
	if note := SessionNote(dir); note != "" {
		t.Errorf("empty shim dir produced a note:\n%s", note)
	}
	if note := SessionNote(""); note != "" {
		t.Errorf("no shim dir produced a note:\n%s", note)
	}
	if note := SessionNote(filepath.Join(dir, "nope")); note != "" {
		t.Errorf("missing shim dir produced a note:\n%s", note)
	}
}
