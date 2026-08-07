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

// The commands block is one declaration serving three readers, so its
// parsing has to drop exactly what ParseManifest drops — a name that
// could escape ShimDir or shadow coop's own plumbing is no safer for
// being written in JSON.
func TestCommandNames(t *testing.T) {
	c := RepoConfig{Commands: map[string]CommandSpec{
		"go":      {},
		"psql":    {Allow: AllowSpec{All: true}},
		"tmux":    {},                            // reserved: would reroute coop's own tmux calls
		"docker":  {Allow: AllowSpec{All: true}}, // reserved: shim -> tools exec -> shim
		"../evil": {},                            // would write a shim outside ShimDir
		"a;b":     {},                            // shell metacharacter
	}}
	names, declared := c.CommandNames()
	if !declared {
		t.Fatal("a present block must report as declared")
	}
	// Sorted, because map order is random and shims must not churn.
	if strings.Join(names, ",") != "go,psql" {
		t.Errorf("CommandNames() = %v", names)
	}
	// allow is the only route to a grant, and it is default-deny.
	if got := strings.Join(c.Allowed(), ","); got != "psql" {
		t.Errorf("Allowed() = %v, want [psql]", c.Allowed())
	}
}

// An absent block and an empty one mean different things: fall back to
// the image, versus shim nothing.
func TestCommandNamesAbsentVersusEmpty(t *testing.T) {
	if _, declared := (RepoConfig{}).CommandNames(); declared {
		t.Error("an absent commands block must not report as declared")
	}
	names, declared := RepoConfig{Commands: map[string]CommandSpec{}}.CommandNames()
	if !declared || len(names) != 0 {
		t.Errorf("an empty block must be declared and empty: %v %v", names, declared)
	}
}

func TestLoadRepoConfigCommands(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"network":"n","commands":{"go":{},"python3":{"allow":true}}}`
	if err := os.WriteFile(RepoConfigPath(repo), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadRepoConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	names, declared := c.CommandNames()
	if !declared || strings.Join(names, ",") != "go,python3" {
		t.Errorf("CommandNames() = %v (declared=%v)", names, declared)
	}
	if strings.Join(c.Allowed(), ",") != "python3" {
		t.Errorf("Allowed() = %v", c.Allowed())
	}
}

// "allow": ["build", "test"] grants one rule per entry, so the routine
// verbs run silently and everything else keeps prompting. What it buys is
// where the prompt lands — go install writing into an invisible $HOME —
// not containment.
func TestAllowNarrowedToArguments(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"commands":{
		"go":    {"allow": ["build", "test", "mod tidy"]},
		"gofmt": {"allow": true},
		"jq":    {}
	}}`
	if err := os.WriteFile(RepoConfigPath(repo), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadRepoConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by command; jq declared but not granted.
	want := "go build,go test,go mod tidy,gofmt"
	if got := strings.Join(c.Allowed(), ","); got != want {
		t.Errorf("Allowed() = %q, want %q", got, want)
	}
}

// An argument prefix is embedded in Bash(<tool> <arg> *), so anything
// that could end the rule early or widen it must not survive. A bad entry
// costs one grant, never the file.
func TestAllowRejectsUnsafeArguments(t *testing.T) {
	spec := CommandSpec{Allow: AllowSpec{Args: []string{
		"build",                  // kept
		"pr list",                // kept: two tokens
		"build ./cmd/x",          // kept: paths are ordinary arguments
		"a) --dangerous",         // would close the rule and append another
		"*",                      // would widen the rule to everything
		"rm -rf ~",               // shell metacharacters
		"a && b",                 //
		"say \"hi\"",             // quotes
		"",                       // empty
		"  leading",              // doubled/leading space
		strings.Repeat("x", 200), // absurd length
	}}}
	want := []string{"go build", "go pr list", "go build ./cmd/x"}
	got := spec.Rules("go")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Rules() = %v, want %v", got, want)
	}
}

// A malformed allow value is an error on the file, not a silent grant of
// everything: "allow": "true" must not read as true.
func TestAllowRejectsWrongType(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"commands":{"go":{"allow":"true"}}}`
	if err := os.WriteFile(RepoConfigPath(repo), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRepoConfig(repo); err == nil {
		t.Fatal("want an error for a string allow value")
	}
}
