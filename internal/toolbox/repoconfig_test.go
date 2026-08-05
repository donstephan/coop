package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	old := userHome
	userHome = func() string { return home }
	t.Cleanup(func() { userHome = old })
	return home
}

func TestLoadRepoConfigMissing(t *testing.T) {
	repo := t.TempDir()
	cfg, err := LoadRepoConfig(repo)
	if err != nil {
		t.Fatalf("absent file should not error: %v", err)
	}
	if cfg.Network != "" || len(cfg.Mounts) != 0 {
		t.Errorf("cfg = %+v, want zero", cfg)
	}
}

func TestLoadRepoConfig(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".coop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"network":"sprocket-v2_default","mounts":["~/.local/bin:rw"]}`
	if err := os.WriteFile(filepath.Join(dir, "toolbox.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRepoConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network != "sprocket-v2_default" {
		t.Errorf("network = %q", cfg.Network)
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0] != "~/.local/bin:rw" {
		t.Errorf("mounts = %v", cfg.Mounts)
	}
}

func TestLoadRepoConfigMalformed(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".coop")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "toolbox.json"), []byte(`{"network":`), 0o644)
	if _, err := LoadRepoConfig(repo); err == nil {
		t.Fatal("want error for unparseable toolbox.json")
	}
}

func TestParseMounts(t *testing.T) {
	home := fakeHome(t)
	got, err := ParseMounts([]string{"~/.local/bin:rw", "~/.aws", "/srv/data:ro"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{
		{Path: filepath.Join(home, ".local", "bin"), RW: true},
		{Path: filepath.Join(home, ".aws"), RW: false},
		{Path: "/srv/data", RW: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mounts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mount %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if a := want[0].Arg(); a != want[0].Path+":"+want[0].Path+":rw" {
		t.Errorf("Arg = %q", a)
	}
	if a := want[1].Arg(); !strings.HasSuffix(a, ":ro") {
		t.Errorf("Arg = %q, want :ro", a)
	}
}

func TestParseMountsRejects(t *testing.T) {
	home := fakeHome(t)
	cases := map[string]string{
		"remapping":      "/srv/data:/workspace",
		"relative":       "data:rw",
		"bad mode":       "/srv/data:rwx",
		"coop config":    "~/.config/coop:rw",
		"coop state":     "~/.local/state/coop",
		"parent of both": "~",
		// A child is the sharper case: this one is another repo's shim
		// directory, whose contents run on the host.
		"child of state":  "~/.local/state/coop/toolbox/beta-0000/bin:rw",
		"child of config": "~/.config/coop/arbiter.md",
	}
	for name, spec := range cases {
		if _, err := ParseMounts([]string{spec}); err == nil {
			t.Errorf("%s: %q was accepted", name, spec)
		}
	}
	// The sibling directory is fine — only coop's own two are refused.
	if _, err := ParseMounts([]string{filepath.Join(home, ".config", "gh")}); err != nil {
		t.Errorf("~/.config/gh should be allowed: %v", err)
	}
}
