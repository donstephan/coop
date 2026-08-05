package toolbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	a := Slug("/home/user/sprocket-v2")
	b := Slug("/home/user/work/sprocket-v2")
	if a == b {
		t.Fatalf("same-basename repos collided: %q", a)
	}
	if !strings.HasPrefix(a, "sprocket-v2-") {
		t.Errorf("slug %q should lead with the basename", a)
	}
	if a != Slug("/home/user/sprocket-v2/") {
		t.Error("trailing slash should not change the slug")
	}
	for _, r := range a {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			t.Fatalf("slug %q has illegal rune %q", a, r)
		}
	}
}

func TestSlugFoldsAwkwardNames(t *testing.T) {
	s := Slug("/home/user/My Repo (v2)")
	if strings.ContainsAny(s, " ()") {
		t.Errorf("slug %q kept punctuation", s)
	}
}

func TestPaths(t *testing.T) {
	home := t.TempDir()
	old := userHome
	userHome = func() string { return home }
	t.Cleanup(func() { userHome = old })

	repo := "/home/user/sprocket-v2"
	slug := Slug(repo)
	want := filepath.Join(home, ".local", "state", "coop", "toolbox", slug)
	if got := ShimDir(repo); got != filepath.Join(want, "bin") {
		t.Errorf("ShimDir = %q", got)
	}
	if got := HomeDir(repo); got != filepath.Join(want, "home") {
		t.Errorf("HomeDir = %q", got)
	}
	if got := ActivityFile(repo); got != filepath.Join(want, "home", ".coop-activity") {
		t.Errorf("ActivityFile = %q", got)
	}
	if got := ContainerName(repo); got != "coop-tools-"+slug {
		t.Errorf("ContainerName = %q", got)
	}
}

func TestPathsWithoutHome(t *testing.T) {
	old := userHome
	userHome = func() string { return "" }
	t.Cleanup(func() { userHome = old })
	if got := ShimDir("/home/user/sprocket-v2"); got != "" {
		t.Errorf("ShimDir = %q, want empty when home is unknown", got)
	}
}
