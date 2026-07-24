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

func TestDefaultPath(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	if got := DefaultPath(); got != "/home/test/.config/coop/config.json" {
		t.Fatalf("DefaultPath() = %q", got)
	}
}
