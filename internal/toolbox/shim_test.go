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
	if _, err := Prepare(e, repo, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(home, ".local", "state", "coop", "toolbox",
		Slug(repo), "bin", "jq")
	if _, err := os.Stat(shim); err != nil {
		t.Fatalf("shim not written: %v", err)
	}
}

// writeRepoConfig writes <repo>/.coop/toolbox.json.
func writeRepoConfig(t *testing.T, repo, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".coop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toolbox.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A declared commands block is the complete list: it replaces the image's
// manifest rather than extending it, so a repo that declares commands and
// omits a base tool does not get that base tool. That is the cost of
// explicit, and it must not quietly stop being true.
func TestPrepareCommandsBlockReplacesManifest(t *testing.T) {
	home := fakeHome(t)
	repo := t.TempDir()
	writeRepoConfig(t, repo, `{"commands":{"go":{},"psql":{"allow":true}}}`)
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	// The image still offers jq. The repo did not declare it, so it must
	// not be shimmed — and the manifest must not even be read.
	e.outputs["run --rm --entrypoint cat "+BaseTag()+" /etc/coop/tools"] = "jq\n"
	if _, err := Prepare(e, repo, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "state", "coop", "toolbox", Slug(repo), "bin")
	for _, tool := range []string{"go", "psql"} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Errorf("declared %s not shimmed: %v", tool, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "jq")); !os.IsNotExist(err) {
		t.Error("undeclared jq was shimmed from the image manifest")
	}
}

// With no commands block the image manifest is the whole story — the
// zero-config path, which must keep working untouched.
func TestPrepareFallsBackToManifest(t *testing.T) {
	home := fakeHome(t)
	repo := t.TempDir()
	writeRepoConfig(t, repo, `{"network":"n"}`)
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	e.outputs["run --rm --entrypoint cat "+BaseTag()+" /etc/coop/tools"] = "jq\npython3\n"
	if _, err := Prepare(e, repo, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "state", "coop", "toolbox", Slug(repo), "bin")
	for _, tool := range []string{"jq", "python3"} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Errorf("%s not shimmed from the image manifest: %v", tool, err)
		}
	}
}

// An empty block is a repo saying "shim nothing", which is not the same
// as saying nothing — it must not fall through to the image.
func TestPrepareEmptyCommandsBlockShimsNothing(t *testing.T) {
	home := fakeHome(t)
	repo := t.TempDir()
	writeRepoConfig(t, repo, `{"commands":{}}`)
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	e.outputs["run --rm --entrypoint cat "+BaseTag()+" /etc/coop/tools"] = "jq\n"
	if _, err := Prepare(e, repo, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "state", "coop", "toolbox", Slug(repo), "bin")
	if _, err := os.Stat(filepath.Join(bin, "jq")); !os.IsNotExist(err) {
		t.Error("an empty commands block fell through to the image manifest")
	}
}

// resolveKey is the fake-engine key for the resolution probe.
func resolveKey(image string, tools ...string) string {
	return "run --rm --entrypoint sh " + image + " -c " +
		"for t in " + strings.Join(tools, " ") +
		`; do command -v "$t" >/dev/null 2>&1 || echo "$t"; done`
}

// The check that replaces co-location: a repo can now declare a command
// in toolbox.json and forget to install it in the Dockerfile, and the
// symptom is a shim that fails at exec rather than anything visible here.
func TestPrepareReportsMissingTools(t *testing.T) {
	home := fakeHome(t)
	repo := t.TempDir()
	writeRepoConfig(t, repo, `{"commands":{"go":{},"psql":{}}}`)
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	e.outputs[resolveKey(BaseTag(), "go", "psql")] = "psql\n"
	missing, err := Prepare(e, repo, "/home/user/bin/coop")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(missing, ",") != "psql" {
		t.Errorf("missing = %v, want [psql]", missing)
	}
	// The shim is still written: skipping it would silently hand psql to
	// the host, which is the failure the declaration exists to prevent.
	bin := filepath.Join(home, ".local", "state", "coop", "toolbox", Slug(repo), "bin")
	if _, err := os.Stat(filepath.Join(bin, "psql")); err != nil {
		t.Errorf("missing tool lost its shim: %v", err)
	}
}

func TestPrepareReportsNothingWhenAllResolve(t *testing.T) {
	repo := t.TempDir()
	writeRepoConfig(t, repo, `{"commands":{"go":{}}}`)
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	e.outputs[resolveKey(BaseTag(), "go")] = "\n"
	missing, err := Prepare(e, repo, "/home/user/bin/coop")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
}

// A diagnostic that cannot run must not stop a session being created.
func TestMissingToolsSwallowsEngineFailure(t *testing.T) {
	e := newFakeEngine() // no output registered: the probe errors
	if got := MissingTools(e, "img", []string{"go"}); got != nil {
		t.Errorf("a failed probe must report nothing, got %v", got)
	}
	if got := MissingTools(e, "img", nil); got != nil {
		t.Errorf("an empty list must not run the probe, got %v", got)
	}
}
