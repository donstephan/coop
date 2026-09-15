package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"coop/internal/toolbox"
)

func writeConfig(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewToolboxCmdRejectsBadIdle(t *testing.T) {
	// Caught at generation, where the answer is still "no shims and an
	// untouched PATH". Left to the first exec it would be every shimmed
	// command in every live session failing on one bad line.
	fn, err := newToolboxCmd(writeConfig(t, `{"toolbox":{"idle_timeout":"soon"}}`))
	if err == nil {
		t.Fatal("an unparseable idle_timeout must fail generation")
	}
	if fn != nil {
		t.Error("a failed generation must not return a PATH rewriter")
	}
}

func TestNewToolboxCmdDisabled(t *testing.T) {
	fn, err := newToolboxCmd(writeConfig(t, `{"toolbox":{"enabled":false}}`))
	if err != nil {
		t.Fatalf("disabled toolbox should not error: %v", err)
	}
	if fn != nil {
		t.Error("a disabled toolbox must not rewrite PATH")
	}
}

func TestIsToolsCmd(t *testing.T) {
	if !isToolsCmd([]string{"tools", "ls"}) {
		t.Error("tools ls should dispatch")
	}
	if isToolsCmd([]string{"-socket", "coop"}) {
		t.Error("flags must not dispatch")
	}
	if isToolsCmd(nil) {
		t.Error("no args must not dispatch")
	}
}

func TestToolsCLIUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runToolsCLI([]string{"tools"}, &out, &errb); code == 0 {
		t.Error("bare 'tools' should be a usage error")
	}
	if !strings.Contains(errb.String(), "exec") {
		t.Errorf("usage should list subcommands: %q", errb.String())
	}
}

func TestToolsCLIUnknownSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runToolsCLI([]string{"tools", "frobnicate"}, &out, &errb); code == 0 {
		t.Error("unknown subcommand should fail")
	}
}

func TestToolsExecNeedsArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runToolsCLI([]string{"tools", "exec", "/home/user/sprocket-v2"}, &out, &errb); code == 0 {
		t.Error("exec without a tool should fail")
	}
}

// coop tools home must print the path alone, so `ls "$(coop tools home)"`
// composes. Anything explanatory belongs on stderr.
func TestToolsHomePrintsPathAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	if code := runToolsCLI([]string{"tools", "home", "/home/user/sprocket-v2"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if want := toolbox.HomeDir("/home/user/sprocket-v2"); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("stdout must be one line, got %q", got)
	}
	// The directory does not exist here, which is a note, not a failure.
	if !strings.Contains(stderr.String(), "not created yet") {
		t.Errorf("stderr = %q, want the not-created note", stderr.String())
	}
}

// A repo argument becomes a slug, a container name and a bind mount, so
// it has to be absolute before any of that. A relative one slugs to
// repo-<hash of "."> — a name belonging to no repo, which builds a second
// image — and hands docker "-v .:.:rw", which it rejects. exec took its
// argument verbatim until this was wired through toolsRepo like the rest.
func TestToolsRepoAbsolutizes(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := toolsRepo([]string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if got != wd {
		t.Errorf("toolsRepo(\".\") = %q, want %q", got, wd)
	}
	// An already-absolute path must pass through unchanged, or the slug
	// of a container already running would shift under it.
	abs := "/home/user/sprocket-v2"
	if got, err := toolsRepo([]string{abs}); err != nil || got != abs {
		t.Errorf("toolsRepo(%q) = %q, %v", abs, got, err)
	}
	// No argument is the working directory.
	if got, err := toolsRepo(nil); err != nil || got != wd {
		t.Errorf("toolsRepo(nil) = %q, %v; want %q", got, err, wd)
	}
}

// grantsFixture writes a repo config and, for each name in shims, a shim
// in that repo's directory under a temp HOME.
func grantsFixture(t *testing.T, cfg string, shims ...string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "sprocket-v2")
	if err := os.MkdirAll(filepath.Join(repo, ".coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolbox.RepoConfigPath(repo), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(shims) > 0 {
		dir := toolbox.ShimDir(repo)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, s := range shims {
			if err := os.WriteFile(filepath.Join(dir, s), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return repo
}

// stdout is the rules alone so the command composes; everything that
// explains the list goes to stderr.
func TestToolsGrantsPrintsRulesAlone(t *testing.T) {
	repo := grantsFixture(t,
		`{"commands":{"go":{"allow":["build","test"]},"gofmt":{"allow":true},"jq":{}}}`,
		"go", "gofmt", "jq")

	var stdout, stderr bytes.Buffer
	if code := runToolsCLI([]string{"tools", "grants", repo}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	want := "Bash(go build *)\nBash(go test *)\nBash(gofmt *)"
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	// jq is declared but carries no allow, so it must not appear.
	if strings.Contains(stdout.String(), "jq") {
		t.Error("an unallowed command reached the rule list")
	}
	// A running session read its settings at launch, which is the other
	// half of "why is this still prompting".
	if !strings.Contains(stderr.String(), "sessions started from now on") {
		t.Errorf("stderr = %q, want the next-session note", stderr.String())
	}
}

// The most useful thing this command says: the rule is withheld because
// the shim is not there yet, not because it was never declared.
func TestToolsGrantsExplainsWithheldRules(t *testing.T) {
	repo := grantsFixture(t, `{"commands":{"go":{"allow":["build"]}}}`) // no shims

	var stdout, stderr bytes.Buffer
	if code := runToolsCLI([]string{"tools", "grants", repo}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("stdout = %q, want no rules", stdout.String())
	}
	for _, want := range []string{"not yet shimmed", "go build", "coop tools rebuild"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, missing %q", stderr.String(), want)
		}
	}
}

// An empty list with nothing withheld means the repo never asked for a
// grant — a different answer, and it has to read differently.
func TestToolsGrantsWhenNothingIsAllowed(t *testing.T) {
	repo := grantsFixture(t, `{"commands":{"go":{},"jq":{}}}`, "go", "jq")

	var stdout, stderr bytes.Buffer
	if code := runToolsCLI([]string{"tools", "grants", repo}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("stdout = %q, want no rules", stdout.String())
	}
	if !strings.Contains(stderr.String(), `no command in .coop/toolbox.json carries "allow"`) {
		t.Errorf("stderr = %q, want the nothing-declared note", stderr.String())
	}
}

// The marker is what someone finds by following a path, so it has to name
// the repo that owns it and the escape hatch for reaching the host.
func TestWriteHomeMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := "/home/user/sprocket-v2"
	dir := toolbox.HomeDir(repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := toolbox.WriteHomeMarker(repo); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(toolbox.HomeMarkerPath(repo))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		repo,                // which repo owns this directory
		"not your own home", // the actual surprise
		"mounts",            // how to make an install reach the host
		"coop tools home",   // how to find it again
	} {
		if !strings.Contains(body, want) {
			t.Errorf("marker is missing %q:\n%s", want, body)
		}
	}
}

// A dotfile would be invisible to the ls that brought someone here.
func TestHomeMarkerIsVisible(t *testing.T) {
	if strings.HasPrefix(toolbox.HomeMarkerName, ".") {
		t.Errorf("marker %q is a dotfile", toolbox.HomeMarkerName)
	}
}

// Phrased as the edits that resolve it, like missingToolsWarning: a bare
// path reads as a coop failure rather than as a repo declaring a path
// this host does not have.
func TestSkippedMountsWarning(t *testing.T) {
	msg := skippedMountsWarning([]toolbox.Mount{
		{Path: "/home/user/workspace/sprocket-v2"},
		{Path: "/srv/data"},
	}).Error()
	for _, want := range []string{"/home/user/workspace/sprocket-v2", "/srv/data", ".coop/toolbox.json"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning = %q, want it to name %q", msg, want)
		}
	}
}
