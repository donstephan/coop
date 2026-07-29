package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadExpandsTilde(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	c, err := Load(write(t, `{"repos": ["~/proj", "/abs/path"]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/test/proj", "/abs/path"}
	if len(c.Repos) != 2 || c.Repos[0] != want[0] || c.Repos[1] != want[1] {
		t.Fatalf("repos = %v, want %v", c.Repos, want)
	}
}

func TestLoadIgnoresUnknownKeys(t *testing.T) {
	c, err := Load(write(t, `{"repos": ["/a"], "future": {"nested": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Repos) != 1 || c.Repos[0] != "/a" {
		t.Fatalf("repos = %v", c.Repos)
	}
}

func TestLoadEmptyObject(t *testing.T) {
	c, err := Load(write(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Repos) != 0 {
		t.Fatalf("repos = %v, want empty", c.Repos)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil || !strings.Contains(err.Error(), "nope.json") {
		t.Fatalf("err = %v, want mention of nope.json", err)
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	path := write(t, `{"repos": [`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want mention of %s", err, path)
	}
}

func TestLoadTmuxOverrides(t *testing.T) {
	c, err := Load(write(t, `{"repos": ["/a"], "tmux": ["set -g mouse off", "bind x kill-pane"]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"set -g mouse off", "bind x kill-pane"}
	if len(c.Tmux) != 2 || c.Tmux[0] != want[0] || c.Tmux[1] != want[1] {
		t.Fatalf("tmux = %v, want %v", c.Tmux, want)
	}
}

func TestSplitCommand(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"set -g mouse off", []string{"set", "-g", "mouse", "off"}},
		{`set -g set-titles-string "#S — #T"`,
			[]string{"set", "-g", "set-titles-string", "#S — #T"}},
		{`set -g foo 'a b'`, []string{"set", "-g", "foo", "a b"}},
		{"  bind   h  switch-client  -t  roost ",
			[]string{"bind", "h", "switch-client", "-t", "roost"}},
	} {
		got, err := SplitCommand(tc.in)
		if err != nil {
			t.Fatalf("SplitCommand(%q): %v", tc.in, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("SplitCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSplitCommandErrors(t *testing.T) {
	for _, in := range []string{`set -g foo "unclosed`, "", "   "} {
		if got, err := SplitCommand(in); err == nil {
			t.Fatalf("SplitCommand(%q) = %v, want error", in, got)
		}
	}
}

func TestLoadArbiterModel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"arbiter":{"model":"opus"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Arbiter.Model != "opus" {
		t.Errorf("model = %q", c.Arbiter.Model)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	if got := DefaultPath(); got != "/home/test/.config/coop/config.json" {
		t.Fatalf("DefaultPath() = %q", got)
	}
}

// repoDir makes a real directory to add, since AddRepo insists the path
// exists — named for a repo, not for anything on the developer's disk.
func repoDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAddRepoAppends(t *testing.T) {
	p := write(t, `{"repos": ["/home/user/alpha"]}`)
	dir := repoDir(t, "sprocket-v2")
	got, err := AddRepo(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("AddRepo returned %q, want %q", got, dir)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/user/alpha", dir}
	if !slices.Equal(c.Repos, want) {
		t.Fatalf("repos = %v, want %v", c.Repos, want)
	}
}

func TestAddRepoKeepsUnknownKeysAndOrder(t *testing.T) {
	p := write(t, `{"tmux": ["set -g mouse off"], "repos": ["/home/user/alpha"], `+
		`"future": {"b": 1, "a": 2}}`)
	if _, err := AddRepo(p, repoDir(t, "sprocket-v2")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`"set -g mouse off"`, `"b": 1`, `"a": 2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("rewrite dropped %s:\n%s", want, body)
		}
	}
	// Nested keys keep source order too — values are re-indented, never
	// re-marshalled through a map.
	if strings.Index(body, `"b"`) > strings.Index(body, `"a": 2`) {
		t.Fatalf("nested keys reordered:\n%s", body)
	}
	if i, j, k := strings.Index(body, `"tmux"`), strings.Index(body, `"repos"`),
		strings.Index(body, `"future"`); !(i < j && j < k) {
		t.Fatalf("top-level keys reordered:\n%s", body)
	}
}

func TestAddRepoStoresTildeAsTyped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "src", "sprocket-v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := write(t, `{"repos": ["~/alpha"]}`)
	dir, err := AddRepo(p, "~/src/sprocket-v2")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "src", "sprocket-v2"); dir != want {
		t.Fatalf("AddRepo returned %q, want %q", dir, want)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Both the existing entry and the new one stay portable.
	for _, want := range []string{`"~/alpha"`, `"~/src/sprocket-v2"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("want %s in:\n%s", want, data)
		}
	}
}

func TestAddRepoDeduplicates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "sprocket-v2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := write(t, `{"repos": ["~/sprocket-v2"]}`)
	// Same directory, spelled absolutely and with a trailing slash.
	got, err := AddRepo(p, dir+"/")
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("AddRepo returned %q, want %q", got, dir)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Repos) != 1 {
		t.Fatalf("repos = %v, want the one entry unduplicated", c.Repos)
	}
}

func TestAddRepoCreatesMissingConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "coop", "config.json")
	dir := repoDir(t, "sprocket-v2")
	if _, err := AddRepo(p, dir); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Repos, []string{dir}) {
		t.Fatalf("repos = %v, want [%s]", c.Repos, dir)
	}
}

func TestAddRepoRejects(t *testing.T) {
	dir := repoDir(t, "sprocket-v2")
	file := filepath.Join(dir, "README.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, path, want string }{
		{"empty", "  ", "empty path"},
		{"relative", "sprocket-v2", "must start with"},
		{"missing", filepath.Join(dir, "nope"), "no such file"},
		{"file", file, "not a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := write(t, `{"repos": []}`)
			if _, err := AddRepo(p, tc.path); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AddRepo(%q) error = %v, want %q", tc.path, err, tc.want)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != `{"repos": []}` {
				t.Fatalf("rejected add rewrote the config:\n%s", data)
			}
		})
	}
}

func TestAddRepoLeavesMalformedConfigAlone(t *testing.T) {
	p := write(t, `{"repos": [`)
	if _, err := AddRepo(p, repoDir(t, "sprocket-v2")); err == nil {
		t.Fatal("AddRepo on a malformed config should fail")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"repos": [` {
		t.Fatalf("malformed config was rewritten:\n%s", data)
	}
}
