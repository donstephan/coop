package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteHookSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "hooks-settings.json")
	if err := WriteHookSettings(path, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	want := []string{"SessionStart", "UserPromptSubmit", "PostToolUse",
		"PermissionRequest", "PermissionDenied", "Notification", "Stop", "SessionEnd"}
	for _, ev := range want {
		entries := s.Hooks[ev]
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatalf("%s: %d entries", ev, len(entries))
		}
		h := entries[0].Hooks[0]
		if h.Type != "command" || h.Command != "'/home/user/bin/coop' hook" || h.Timeout != 5 {
			t.Errorf("%s hook = %+v", ev, h)
		}
	}
	if len(s.Hooks) != len(want) {
		t.Errorf("hooks for %d events, want %d", len(s.Hooks), len(want))
	}
}

// The grants ride in the same injected file as the hooks — verified
// against a real claude, where a Bash rule delivered by --settings turned
// a blocked call into an executed one and the hook still fired.
func TestWriteSessionSettingsCarriesGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "settings.json")
	if err := WriteSessionSettings(path, "/home/user/bin/coop", []string{"go", "gofmt"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks       map[string]json.RawMessage `json:"hooks"`
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	// The wildcard form, which is what actually matches an invocation
	// with arguments.
	want := []string{"Bash(go *)", "Bash(gofmt *)"}
	if len(s.Permissions.Allow) != len(want) {
		t.Fatalf("allow = %v, want %v", s.Permissions.Allow, want)
	}
	for i, w := range want {
		if s.Permissions.Allow[i] != w {
			t.Errorf("allow[%d] = %q, want %q", i, s.Permissions.Allow[i], w)
		}
	}
	// Grants must not cost the session its status tier.
	if len(s.Hooks) != len(hookEvents) {
		t.Errorf("hooks for %d events, want %d", len(s.Hooks), len(hookEvents))
	}
}

// A session on a repo with no grants has to look exactly like a session
// from before grants existed — an empty allow list is a different
// document, and permissions is the one key worth not writing on spec.
func TestWriteSessionSettingsOmitsEmptyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := WriteSessionSettings(path, "/home/user/bin/coop", nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if _, ok := s["permissions"]; ok {
		t.Error("permissions written for a repo with no grants")
	}
	if _, ok := s["hooks"]; !ok {
		t.Error("hooks missing")
	}
}

func TestWriteHookSettingsOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-settings.json")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteHookSettings(path, "/home/user/bin/coop"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "stale" {
		t.Error("existing file not regenerated")
	}
}

func TestWithHookSettings(t *testing.T) {
	got := WithHookSettings("claude --continue", "/home/user/x y.json")
	if got != "claude --continue --settings '/home/user/x y.json'" {
		t.Errorf("got %q", got)
	}
}
