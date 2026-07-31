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
