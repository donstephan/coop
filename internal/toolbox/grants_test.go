package toolbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// grantFixture points the state tree at a temp home and writes repo's
// .coop/toolbox.json, returning the repo path. shims are the tool names
// to create in the repo's shim directory.
func grantFixture(t *testing.T, cfg string, shims ...string) string {
	t.Helper()
	home := t.TempDir()
	old := userHome
	userHome = func() string { return home }
	t.Cleanup(func() { userHome = old })

	repo := filepath.Join(t.TempDir(), "sprocket-v2")
	if err := os.MkdirAll(filepath.Join(repo, ".coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cfg != "" {
		if err := os.WriteFile(RepoConfigPath(repo), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(shims) > 0 {
		dir := ShimDir(repo)
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

func TestGrantsAreDeclaredIntersectShimmed(t *testing.T) {
	cfg := `{"commands":{
		"go":     {"allow": true},
		"gofmt":  {"allow": true},
		"psql":   {}
	}}`
	// gofmt is declared with allow but has no shim yet; psql is shimmed
	// but was never granted.
	repo := grantFixture(t, cfg, "go", "psql")

	got := Grants(repo)
	if want := []string{"go"}; !slices.Equal(got, want) {
		t.Errorf("Grants = %v, want %v", got, want)
	}
}

// A narrowed grant is filtered by the same shim its whole-command form
// would have been: one missing shim drops every rule for that command,
// not just the first.
func TestGrantsNarrowedByArgument(t *testing.T) {
	cfg := `{"commands":{
		"go":        {"allow": ["build", "test"]},
		"terraform": {"allow": ["plan"]}
	}}`
	repo := grantFixture(t, cfg, "go") // no terraform shim

	got := Grants(repo)
	if want := []string{"go build", "go test"}; !slices.Equal(got, want) {
		t.Errorf("Grants = %v, want %v", got, want)
	}
}

// The case that matters most: a fresh clone launches before Prepare has
// written anything, and a grant with no shim behind it is the host's copy
// running unattended.
func TestGrantsEmptyBeforeShimsExist(t *testing.T) {
	repo := grantFixture(t, `{"commands":{"go":{"allow":true}}}`)
	if got := Grants(repo); len(got) > 0 {
		t.Errorf("Grants = %v, want none before the shims are written", got)
	}
}

// Declared-but-not-allowed is the default. Shimming a command must never
// be enough to grant it.
func TestGrantsDefaultDeny(t *testing.T) {
	repo := grantFixture(t, `{"commands":{"go":{},"psql":{}}}`, "go", "psql")
	if got := Grants(repo); len(got) > 0 {
		t.Errorf("Grants = %v, want none without \"allow\": true", got)
	}
}

// A repo with no .coop/toolbox.json has declared nothing, so there is
// nothing to grant even if a stale shim directory survives.
func TestGrantsWithoutConfig(t *testing.T) {
	repo := grantFixture(t, "", "go")
	if got := Grants(repo); len(got) > 0 {
		t.Errorf("Grants = %v, want none without a config", got)
	}
}

// A directory named like a tool is not a shim, and exec would not treat
// it as one either.
func TestGrantsIgnoreDirectories(t *testing.T) {
	repo := grantFixture(t, `{"commands":{"go":{"allow":true}}}`)
	if err := os.MkdirAll(filepath.Join(ShimDir(repo), "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Grants(repo); len(got) > 0 {
		t.Errorf("Grants = %v, want none for a directory", got)
	}
}

func TestSettingsPathWithoutHome(t *testing.T) {
	old := userHome
	userHome = func() string { return "" }
	t.Cleanup(func() { userHome = old })
	if got := SettingsPath("/home/user/sprocket-v2"); got != "" {
		t.Errorf("SettingsPath = %q, want empty when home is unknown", got)
	}
}
