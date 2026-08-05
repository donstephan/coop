// internal/toolbox/image_test.go
package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseTagStable(t *testing.T) {
	first, second := BaseTag(), BaseTag()
	if first != second {
		t.Fatalf("BaseTag is not deterministic: %q then %q", first, second)
	}
	if !strings.HasPrefix(BaseTag(), "coop-tools:base-") {
		t.Errorf("BaseTag = %q", BaseTag())
	}
}

func TestImageTagNoOverlay(t *testing.T) {
	repo := t.TempDir()
	tag, err := ImageTag(repo)
	if err != nil {
		t.Fatal(err)
	}
	if tag != BaseTag() {
		t.Errorf("tag = %q, want the base tag", tag)
	}
}

func TestImageTagWithOverlay(t *testing.T) {
	repo := t.TempDir()
	writeOverlay(t, repo, "FROM coop-tools:base\nRUN echo mongosh >> /etc/coop/tools\n")
	tag, err := ImageTag(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tag, "coop-tools-"+Slug(repo)+":") {
		t.Errorf("tag = %q", tag)
	}
	first := tag
	writeOverlay(t, repo, "FROM coop-tools:base\nRUN echo psql >> /etc/coop/tools\n")
	tag2, err := ImageTag(repo)
	if err != nil {
		t.Fatal(err)
	}
	if tag2 == first {
		t.Error("editing the overlay must change the tag")
	}
}

func TestEnsureImageBuildsBaseOnce(t *testing.T) {
	// The base build writes a scratch Dockerfile through os.MkdirTemp,
	// which resolves against $TMPDIR; pointing that at t.TempDir() keeps
	// the one test that reaches the build path from writing outside it.
	t.Setenv("TMPDIR", t.TempDir())
	repo := t.TempDir()
	e := newFakeEngine()
	e.errs["image inspect "+BaseTag()] = errNoImage{}
	tag, err := EnsureImage(e, repo)
	if err != nil {
		t.Fatal(err)
	}
	if tag != BaseTag() {
		t.Errorf("tag = %q", tag)
	}
	if !e.called("build") {
		t.Fatalf("base was not built: %s", e.lastCall())
	}
	if !e.called("tag", BaseTag(), BaseMovingTag) {
		t.Errorf("moving tag not updated: %v", e.calls)
	}
}

func TestEnsureImageSkipsExistingBase(t *testing.T) {
	repo := t.TempDir()
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	if _, err := EnsureImage(e, repo); err != nil {
		t.Fatal(err)
	}
	if e.called("build") {
		t.Errorf("existing base should not rebuild: %v", e.calls)
	}
}

func TestEnsureImageBuildsOverlay(t *testing.T) {
	repo := t.TempDir()
	writeOverlay(t, repo, "FROM coop-tools:base\nRUN echo mongosh >> /etc/coop/tools\n")
	e := newFakeEngine()
	e.outputs["image inspect "+BaseTag()] = "sha256:abc"
	tag, err := EnsureImage(e, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !e.called("build", "-t", tag) {
		t.Fatalf("overlay not built as %q: %v", tag, e.calls)
	}
}

func writeOverlay(t *testing.T, repo, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".coop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools.Dockerfile"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// errNoImage stands in for docker's "No such image" exit.
type errNoImage struct{}

func (errNoImage) Error() string { return "No such image" }
