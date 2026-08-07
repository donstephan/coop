package toolbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RepoConfig is <repo>/.coop/toolbox.json: the things a Dockerfile
// cannot state — the network the container joins, the host paths it can
// see, and the repo's explicit command declaration.
type RepoConfig struct {
	Network  string                 `json:"network"`
	Mounts   []string               `json:"mounts"`
	Commands map[string]CommandSpec `json:"commands"`
}

// CommandSpec is what a repo says about one declared command. Being
// declared at all is what shims it; Allow is the only switch, and it is
// the only route to a Bash(...) grant in the settings coop injects.
type CommandSpec struct {
	Allow AllowSpec `json:"allow"`
}

// AllowSpec is `"allow": true` or `"allow": ["build", "test"]`.
//
// The list form grants one rule per entry — Bash(go build *) rather than
// Bash(go *) — and everything it does not name keeps prompting. What that
// buys is **where the prompt lands**, not containment: `go test` compiles
// and runs whatever is in the tree, and a test that shells out to
// `go install` gets there regardless, so narrowing a compiler restricts
// nothing. It is worth doing anyway because the commands worth prompting
// on are rarely the dangerous ones — they are the surprising ones.
// `go install` succeeds, works for the rest of that command, and writes
// into a container $HOME invisible from the host; a prompt is exactly the
// moment a human says "that is not going where you think".
//
// So: grant the inner loop, let the unusual verbs ask. On a service CLI
// (terraform plan vs apply, gh pr list vs merge) the same shape does draw
// a real line, because neither subcommand can reach the other.
type AllowSpec struct {
	All  bool     // "allow": true — the whole command
	Args []string // "allow": ["build", "test"] — one rule per entry
}

func (a *AllowSpec) UnmarshalJSON(b []byte) error {
	var all bool
	if err := json.Unmarshal(b, &all); err == nil {
		a.All = all
		return nil
	}
	var args []string
	if err := json.Unmarshal(b, &args); err != nil {
		return fmt.Errorf("allow must be true/false or a list of argument prefixes: %w", err)
	}
	a.Args = args
	return nil
}

// Rules renders the Bash(...) rule bodies this spec grants for tool —
// "go" for the whole command, "go build" and "go test" for a list. The
// caller wraps each in Bash(<body> *); a two-token prefix matches an
// invocation with arguments the same way a one-token prefix does.
//
// Unsafe argument prefixes are dropped rather than failing the file, the
// same way ParseManifest drops a bad manifest line: one missing grant
// costs a prompt, where refusing the whole config would cost every grant
// the repo has.
func (s CommandSpec) Rules(tool string) []string {
	if s.Allow.All {
		return []string{tool}
	}
	out := make([]string, 0, len(s.Allow.Args))
	for _, arg := range s.Allow.Args {
		if safeArg(arg) {
			out = append(out, tool+" "+arg)
		}
	}
	return out
}

// safeArg accepts an argument prefix that can be embedded in a
// Bash(<tool> <arg> *) rule without changing its shape. Tokens of
// [A-Za-z0-9._/-] separated by single spaces: enough for "plan",
// "pr list" and "build ./cmd/coop", and short of anything — a ")" ending
// the rule early, a "*" widening it, a quote or shell metacharacter —
// that would make the written rule mean more than the file says.
func safeArg(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, field := range strings.Split(s, " ") {
		if field == "" { // leading, trailing or doubled space
			return false
		}
		for _, r := range field {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '.', r == '_', r == '-', r == '/':
			default:
				return false
			}
		}
	}
	return true
}

// Commands returns the declared command names in sorted order, dropping
// the same unsafe and reserved names ParseManifest drops. Sorted because
// map order is random and shim generation must not churn.
//
// The second result reports whether the repo declared a commands block at
// all — an empty block is a repo saying "shim nothing", which is not the
// same as saying nothing.
func (c RepoConfig) CommandNames() ([]string, bool) {
	if c.Commands == nil {
		return nil, false
	}
	out := make([]string, 0, len(c.Commands))
	for name := range c.Commands {
		if !safeTool(name) || reservedTools[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, true
}

// Allowed returns the Bash(...) rule bodies the repo grants, sorted by
// command — "go" for a whole-command grant, "go build" and "go test" for
// a narrowed one. Everything else declared is shimmed and still prompts.
//
// Rule bodies rather than command names because a command can now yield
// more than one rule. Callers that need to know which command a rule came
// from (Grants, checking the shim exists) walk CommandNames instead.
func (c RepoConfig) Allowed() []string {
	names, _ := c.CommandNames()
	out := names[:0:0]
	for _, n := range names {
		out = append(out, c.Commands[n].Rules(n)...)
	}
	return out
}

// Mount is one resolved host path the container sees. There is no
// destination field on purpose: a mount appears at the same path it has
// on the host, because path identity is what lets a traceback, a config
// file's absolute path and a later Read all name the same file.
type Mount struct {
	Path string
	RW   bool
}

// Arg renders the -v value.
func (m Mount) Arg() string {
	mode := "ro"
	if m.RW {
		mode = "rw"
	}
	return m.Path + ":" + m.Path + ":" + mode
}

// RepoConfigPath is <repo>/.coop/toolbox.json.
func RepoConfigPath(repo string) string {
	return filepath.Join(repo, ".coop", "toolbox.json")
}

// LoadRepoConfig reads the repo's toolbox.json. An absent file is the
// common case and reads as a zero config; a present but unparseable one
// is an error, because guessing at what a malformed mounts list meant is
// how a repo silently loses a mount it declared.
func LoadRepoConfig(repo string) (RepoConfig, error) {
	path := RepoConfigPath(repo)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return RepoConfig{}, nil
	}
	if err != nil {
		return RepoConfig{}, err
	}
	var c RepoConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return RepoConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ParseMounts validates and resolves the declared mounts.
func ParseMounts(specs []string) ([]Mount, error) {
	var out []Mount
	for _, spec := range specs {
		m, err := parseMount(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func parseMount(spec string) (Mount, error) {
	spec = strings.TrimSpace(spec)
	path, mode := spec, "ro"
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		path, mode = spec[:i], spec[i+1:]
	}
	switch mode {
	case "ro", "rw":
	default:
		// A "dst" here is the remapping form, which the design refuses:
		// naming a different path inside would reintroduce exactly the
		// /workspace translation problem path identity removes.
		return Mount{}, fmt.Errorf("mount %q: expected \"<path>\" or \"<path>:ro\" or \"<path>:rw\"", spec)
	}
	if path != "~" && !strings.HasPrefix(path, "~/") && !filepath.IsAbs(path) {
		return Mount{}, fmt.Errorf("mount %q: path must start with / or ~/", spec)
	}
	abs := filepath.Clean(expandHome(path))
	if err := refuseCoopDirs(abs); err != nil {
		return Mount{}, fmt.Errorf("mount %q: %w", spec, err)
	}
	return Mount{Path: abs, RW: mode == "rw"}, nil
}

// refuseCoopDirs rejects a mount covering coop's own config or state
// directory. Not as a boundary — a session with code execution writes
// those directly on the host, so this stops nothing determined. It is
// the reasoning behind the arbiter's gates: it stops an unattended
// own-goal where a container rewrites arbiter.md or the claims directory
// by accident, and it costs one check.
//
// A parent counts, since mounting ~ would carry both. So does a child,
// and that is the sharper case of the two: ~/.local/state/coop/toolbox/
// <slug>/bin holds *another* repo's shims, which are #!/bin/sh scripts
// that run on the host the moment that repo's session calls python3, and
// ~/.config/coop/arbiter.md is the policy the judge obeys. Naming either
// one directly is no safer than naming the directory above it.
func refuseCoopDirs(abs string) error {
	home := userHome()
	if home == "" {
		return nil
	}
	for _, d := range []string{
		filepath.Join(home, ".config", "coop"),
		filepath.Join(home, ".local", "state", "coop"),
	} {
		if abs == d || within(d, abs) || within(abs, d) {
			return fmt.Errorf("%s is coop's own directory", d)
		}
	}
	return nil
}

// within reports whether p is inside dir.
func within(p, dir string) bool {
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home := userHome()
	if home == "" {
		return p
	}
	return filepath.Join(home, p[1:])
}
