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
// sessions, registering coop hook for every event and nothing else. It is
// the fallback shape, used for a session whose repo has no toolbox to
// derive grants from.
func WriteHookSettings(path, exe string) error {
	return WriteSessionSettings(path, exe, nil)
}

// WriteSessionSettings writes the settings file injected into launched
// sessions: coop hook on every event, plus a Bash(<tool> *) rule for each
// name in allow. It overwrites unconditionally, because the file is coop
// infrastructure regenerated from the running binary — deliberately
// unlike ArbiterHome's seed-once, user-owned arbiter.md.
//
// The grants live here, in a file coop owns and injects, rather than in
// the repo's committed .claude/settings.json. That is the whole reason
// there is no PreToolUse guard to install: a committed grant also reaches
// a claude started outside coop, where no shim is on PATH and the rule
// means "run the host's copy unattended", so it needs something to
// re-check that assumption at call time. An injected grant reaches only
// the sessions coop launched, and the caller derives allow from the shims
// that exist (toolbox.Grants), so the grant and the tool it grants are
// written by the same act and cannot desynchronize.
//
// Verified rather than assumed, the way "a PreToolUse hook outranks an
// allow rule" was: a Bash(...) rule delivered by --settings turns a
// blocked Bash call into an executed one, the Bash(<tool> *) wildcard
// form matches, and hooks and permissions coexist in one file with the
// hooks still firing.
func WriteSessionSettings(path, exe string, allow []string) error {
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
	doc := map[string]any{"hooks": hooks}
	// Omitted entirely rather than written empty: a session on a repo with
	// no grants must look exactly like a session from before this existed.
	if len(allow) > 0 {
		rules := make([]string, 0, len(allow))
		for _, body := range allow {
			rules = append(rules, BashRule(body))
		}
		doc["permissions"] = map[string]any{"allow": rules}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// BashRule renders a rule body ("go", "go build") as the permission rule
// written into the injected settings. One definition, because `coop tools
// grants` prints what this writes — a second copy of the format string
// would let the two drift and make the debug aid quietly wrong.
func BashRule(body string) string {
	return "Bash(" + body + " *)"
}

// WithHookSettings appends the --settings flag to claudeCmd, which may
// carry its own flags ("claude --continue") — ours append after, so the
// user's command stays intact whatever it already says.
func WithHookSettings(claudeCmd, path string) string {
	return claudeCmd + " --settings " + shellQuote(path)
}
