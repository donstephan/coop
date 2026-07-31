package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// hookEvents is every Claude Code hook event coop hook subscribes to —
// keep in step with hookAction's cases.
var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PostToolUse", "PermissionRequest",
	"PermissionDenied", "Notification", "Stop", "SessionEnd",
}

// DefaultHookSettingsPath is $XDG_STATE_HOME/coop/hooks-settings.json,
// falling back to ~/.local/state (same convention as DefaultAuditPath).
// "" disables injection — a machine with no resolvable home.
func DefaultHookSettingsPath() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "coop", "hooks-settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "coop", "hooks-settings.json")
}

// WriteHookSettings writes the settings file injected into launched
// sessions, registering coop hook for every event. It overwrites
// unconditionally: the file is coop infrastructure, regenerated each
// hub launch so it always names the running binary — deliberately
// unlike ArbiterHome's seed-once user-owned settings.
func WriteHookSettings(path, exe string) error {
	type hookCmd struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"` // seconds; a wedged tmux must not stall the session
	}
	type entry struct {
		Hooks []hookCmd `json:"hooks"`
	}
	hooks := map[string][]entry{}
	for _, ev := range hookEvents {
		hooks[ev] = []entry{{Hooks: []hookCmd{
			{Type: "command", Command: shellQuote(exe) + " hook", Timeout: 5},
		}}}
	}
	raw, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// WithHookSettings appends the --settings flag to claudeCmd, which may
// carry its own flags ("claude --continue") — ours append after, like
// ArbiterCmd's.
func WithHookSettings(claudeCmd, path string) string {
	return claudeCmd + " --settings " + shellQuote(path)
}
