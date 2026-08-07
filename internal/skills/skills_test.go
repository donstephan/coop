package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"coop/docs"
)

func TestWritePlugin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "coop", "plugin")
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}

	// The manifest must land under the dot-prefixed directory, which is
	// the half a plain //go:embed would silently drop.
	raw, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	// The plugin name is the namespace: /coop:init.
	if m.Name != "coop" {
		t.Errorf("plugin name = %q, want %q", m.Name, "coop")
	}

	skill, err := os.ReadFile(filepath.Join(dir, "skills", "init", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "name: init") {
		t.Error("SKILL.md is missing its name frontmatter")
	}
}

// The reference is docs/toolbox.md itself, not a copy of its rules —
// that identity is the whole anti-drift argument, so assert it.
func TestWritePluginEmbedsDocs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(referencePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != docs.Toolbox {
		t.Error("reference file is not docs/toolbox.md verbatim")
	}
	if len(got) == 0 {
		t.Error("reference file is empty")
	}
}

// SKILL.md points at the reference by a path relative to its own
// directory, so the two must agree.
func TestReferenceIsReachableFromSkill(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(dir, "skills", "init", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "references/toolbox.md") {
		t.Fatal("SKILL.md does not name references/toolbox.md")
	}
	rel, err := filepath.Rel(filepath.Join(dir, "skills", "init"),
		filepath.Join(dir, filepath.FromSlash(referencePath)))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(rel) != "references/toolbox.md" {
		t.Errorf("reference resolves to %q from the skill directory", rel)
	}
}

// The skill writes .coop/ and nothing else. Grants reach a session
// through the settings file coop injects, so there is no repo-side
// permission file to write and no PreToolUse guard to install — and a
// skill that reintroduced either would be writing the harness's own
// control surface for a job coop already does.
func TestSkillWritesNothingInClaudeDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	initDir := filepath.Join(dir, "skills", "init")
	raw, err := os.ReadFile(filepath.Join(initDir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(raw)

	// Stated as an instruction, not merely implied by omission: the model
	// reads this file looking for the step, and silence reads as an
	// oversight it should helpfully correct.
	for _, want := range []string{
		"Never write `.claude/settings.json` or `.claude/hooks/*`",
		"no `cat >`", // the Bash workaround, named so it is refused
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("SKILL.md does not forbid writing .claude/: missing %q", want)
		}
	}
	// The guard is gone. Any surviving reference would send the model
	// looking for an asset that no longer ships.
	for _, gone := range []string{"coop-guard.sh", "PreToolUse"} {
		if strings.Contains(skill, gone) {
			t.Errorf("SKILL.md still references the removed guard: %q", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(initDir, "assets", "coop-guard.sh")); err == nil {
		t.Error("the guard asset still ships in the plugin")
	}
}

// The declaration model is the whole skill: one block that decides what
// is shimmed, with allow as a default-deny switch on top of it.
func TestSkillStatesTheDeclarationModel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "skills", "init", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(raw)
	if !strings.Contains(skill, `"commands"`) {
		t.Error("SKILL.md never tells the model to write the commands block")
	}
	// Default-deny has to be stated, or the model grants what it shims.
	if !strings.Contains(skill, `"allow": true`) {
		t.Error("SKILL.md does not state that allow is the only route to a grant")
	}
	// The stale second list must not creep back in.
	if strings.Contains(skill, ".coop/guarded") {
		t.Error(".coop/guarded is gone; something still references it")
	}
}

// Shims and grants are both resolved at session create, so the session
// that ran the skill keeps prompting until it is restarted. Untold, that
// reads as the setup having silently failed.
func TestSkillSaysGrantsApplyNextSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "skills", "init", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "next session") {
		t.Error("SKILL.md does not tell the user grants apply on the next session")
	}
}

// A skill renamed across coop versions must not leave a second copy
// loaded, so the write replaces rather than merges.
func TestWritePluginReplaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	stale := filepath.Join(dir, "skills", "gone", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a skill from an earlier version survived the rewrite")
	}
}

// The build-then-rename must not leave its scratch directory behind, or
// every hub launch would add one beside the plugin.
func TestWritePluginLeavesNoTemp(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "plugin")
	for range 3 {
		if err := WritePlugin(dir); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "plugin" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("state directory holds %v, want just [plugin]", names)
	}
}

func TestWithPlugin(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		dir  string
		want string
	}{
		{
			name: "plain",
			cmd:  "claude",
			dir:  "/home/user/.local/state/coop/plugin",
			want: "claude --plugin-dir '/home/user/.local/state/coop/plugin'" +
				" --add-dir '/home/user/.local/state/coop/plugin'",
		},
		{
			// The user's own flags must survive — ours append after.
			name: "existing flags",
			cmd:  "claude --continue",
			dir:  "/home/user/.local/state/coop/plugin",
			want: "claude --continue --plugin-dir '/home/user/.local/state/coop/plugin'" +
				" --add-dir '/home/user/.local/state/coop/plugin'",
		},
		{
			name: "quote in path",
			cmd:  "claude",
			dir:  "/home/user/it's/plugin",
			want: `claude --plugin-dir '/home/user/it'\''s/plugin'` +
				` --add-dir '/home/user/it'\''s/plugin'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithPlugin(tt.cmd, tt.dir); got != tt.want {
				t.Errorf("WithPlugin() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --add-dir is variadic: a flag appended after it is swallowed as another
// directory, so it has to be the last thing on the command.
func TestWithPluginKeepsAddDirLast(t *testing.T) {
	got := WithPlugin("claude", "/state/coop/plugin")
	fields := strings.Fields(got)
	if len(fields) < 2 || fields[len(fields)-2] != "--add-dir" {
		t.Errorf("--add-dir is not the final flag: %q", got)
	}
}

// The scope granted is the plugin directory alone. Widening it to the
// parent would hand a monitored session the claims directory and the
// audit log — the record that exists to survive a judge misfiring.
func TestWithPluginScopesToPluginDirOnly(t *testing.T) {
	got := WithPlugin("claude", "/home/user/.local/state/coop/plugin")
	if strings.Contains(got, "'/home/user/.local/state/coop'") {
		t.Errorf("scope widened past the plugin directory: %q", got)
	}
}

// Composes with the hook settings flag rather than replacing it — a
// launched session carries both.
func TestWithPluginComposesWithOtherFlags(t *testing.T) {
	cmd := WithPlugin("claude --settings '/state/coop/hooks-settings.json'", "/state/coop/plugin")
	if !strings.Contains(cmd, "--settings") || !strings.Contains(cmd, "--plugin-dir") {
		t.Errorf("lost a flag: %q", cmd)
	}
}

func TestDefaultPluginDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/home/user/.state")
	if got, want := DefaultPluginDir(), "/home/user/.state/coop/plugin"; got != want {
		t.Errorf("DefaultPluginDir() = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home")
	}
	if got, want := DefaultPluginDir(), filepath.Join(home, ".local", "state", "coop", "plugin"); got != want {
		t.Errorf("DefaultPluginDir() = %q, want %q", got, want)
	}
}
