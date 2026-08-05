// internal/toolbox/shim_test.go
package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifest(t *testing.T) {
	got := ParseManifest("python3\n\n# a comment\npip3\n  jq  \n")
	want := []string{"python3", "pip3", "jq"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseManifestRejectsUnsafe(t *testing.T) {
	got := ParseManifest("../../evil\nrm -rf /\n$(whoami)\nok-tool\n/usr/bin/x\n")
	if strings.Join(got, ",") != "ok-tool" {
		t.Errorf("got %v, want only ok-tool", got)
	}
}

func TestParseManifestDropsReserved(t *testing.T) {
	// A shim for any of these reroutes coop's own plumbing instead of
	// failing: tmux is what internal/hub shells out to from inside the
	// monitored pane, and the engine names are what coop tools exec
	// resolves through the very PATH the shim is on.
	got := ParseManifest("tmux\ndocker\npodman\ncoop\npython3\n")
	if strings.Join(got, ",") != "python3" {
		t.Errorf("got %v, want only python3", got)
	}
}

func TestHostPathDropsShimDirs(t *testing.T) {
	home := fakeHome(t)
	shim := ShimDir("/home/user/sprocket-v2")
	other := filepath.Join(home, ".local", "state", "coop", "toolbox", "beta-0000", "bin")
	in := strings.Join([]string{shim, "/usr/local/bin", other, "/usr/bin"}, ":")
	if got := HostPath(in); got != "/usr/local/bin:/usr/bin" {
		t.Errorf("HostPath = %q", got)
	}
}

func TestLookHostSkipsShim(t *testing.T) {
	fakeHome(t)
	shim := ShimDir("/home/user/sprocket-v2")
	if err := WriteShims(shim, "/home/user/bin/coop", "/home/user/sprocket-v2",
		[]string{"jq"}); err != nil {
		t.Fatal(err)
	}
	hostdir := t.TempDir()
	host := filepath.Join(hostdir, "jq")
	if err := os.WriteFile(host, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+":"+hostdir)
	got, err := LookHost("jq")
	if err != nil {
		t.Fatal(err)
	}
	if got != host {
		t.Errorf("LookHost = %q, want the host copy %q", got, host)
	}
	if _, err := LookHost("definitely-not-installed"); err == nil {
		t.Error("a tool with no host copy must not resolve")
	}
}

func TestRenderShim(t *testing.T) {
	s := RenderShim("/home/user/.local/bin/coop", "/home/user/sprocket-v2", "python3")
	if !strings.HasPrefix(s, "#!/bin/sh\n") {
		t.Errorf("no shebang: %q", s)
	}
	want := `exec '/home/user/.local/bin/coop' tools exec '/home/user/sprocket-v2' 'python3' "$@"`
	if !strings.Contains(s, want) {
		t.Errorf("shim body = %q, want it to contain %q", s, want)
	}
}

func TestRenderShimQuotesAwkwardPaths(t *testing.T) {
	s := RenderShim("/home/user/bin/coop", "/home/user/it's a repo", "jq")
	if strings.Contains(s, "it's a repo'") && !strings.Contains(s, `'\''`) {
		t.Errorf("single quote not escaped: %q", s)
	}
}

func TestWriteShims(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := WriteShims(dir, "/home/user/bin/coop", "/home/user/sprocket-v2",
		[]string{"python3", "jq"}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"python3", "jq"} {
		fi, err := os.Stat(filepath.Join(dir, tool))
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if fi.Mode()&0o111 == 0 {
			t.Errorf("%s is not executable (%v)", tool, fi.Mode())
		}
	}
}

func TestWriteShimsRemovesStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := WriteShims(dir, "/home/user/bin/coop", "/home/user/sprocket-v2",
		[]string{"python3", "mongosh"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteShims(dir, "/home/user/bin/coop", "/home/user/sprocket-v2",
		[]string{"python3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mongosh")); err == nil {
		t.Error("a tool dropped from the manifest must lose its shim")
	}
}

func TestWithToolbox(t *testing.T) {
	got := WithToolbox("claude --continue", "/home/user/.local/state/coop/toolbox/x/bin")
	want := `env PATH='/home/user/.local/state/coop/toolbox/x/bin':"$PATH" claude --continue`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if WithToolbox("claude", "") != "claude" {
		t.Error("an empty shim dir must leave the command alone")
	}
}

func TestReadManifest(t *testing.T) {
	e := newFakeEngine()
	e.outputs["run --rm --entrypoint cat coop-tools:base-x /etc/coop/tools"] = "jq\npython3\n"
	got, err := ReadManifest(e, "coop-tools:base-x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "jq,python3" {
		t.Errorf("got %v", got)
	}
}

func TestPrepareWritesShims(t *testing.T) {
	home := fakeHome(t)
	repo := t.TempDir()
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	e.outputs["run --rm --entrypoint cat "+BaseTag()+" /etc/coop/tools"] = "jq\n"
	if err := Prepare(e, repo, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(home, ".local", "state", "coop", "toolbox",
		Slug(repo), "bin", "jq")
	if _, err := os.Stat(shim); err != nil {
		t.Fatalf("shim not written: %v", err)
	}
}
