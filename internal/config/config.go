// Package config reads the coop config file: the repos the TUI can
// create Claude Code sessions from.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is ~/.config/coop/config.json. Unknown keys are ignored —
// headroom for future settings.
type Config struct {
	Repos []string `json:"repos"`
	// Tmux lists extra tmux commands ("set -g mouse off") chained after
	// coop's built-in defaults at server start, so they win.
	Tmux []string `json:"tmux"`
	// Arbiter configures the a-key arbiter session.
	Arbiter struct {
		Model string `json:"model"` // claude model id/alias; "" = sonnet
	} `json:"arbiter"`
}

// DefaultPath is ~/.config/coop/config.json ("" if home is unknown).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "coop", "config.json")
}

// Load reads and parses the config, expanding a leading "~/" in each
// repo path. Errors always name the file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	for i, r := range c.Repos {
		c.Repos[i] = expandHome(r)
	}
	return c, nil
}

// AddRepo appends repo to the config's "repos" list and returns the
// directory it resolves to. The path is stored as typed — a leading "~/"
// stays "~/", so the config keeps working on another machine — but it
// must name an existing directory, and one already listed is not added
// twice (its directory is still returned, so the caller can go ahead and
// use it).
//
// The file is rewritten from its own raw JSON, key by key in the order
// it already had, rather than re-marshalled from Config: settings coop
// does not know about survive the write. A file that does not parse is
// never rewritten — that would clobber whatever the operator meant.
func AddRepo(path, repo string) (string, error) {
	if path == "" {
		return "", errors.New("no config path")
	}
	repo = filepath.Clean(strings.TrimSpace(repo))
	if repo == "." {
		return "", errors.New("empty path")
	}
	if repo != "~" && !strings.HasPrefix(repo, "~/") && !filepath.IsAbs(repo) {
		return "", fmt.Errorf("%s: path must start with / or ~/", repo)
	}
	dir := expandHome(repo)
	fi, err := os.Stat(dir)
	if err != nil {
		return "", err // already names the path: "stat /x: no such file…"
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s: not a directory", dir)
	}

	obj, keys, err := readObject(path)
	if err != nil {
		return "", err
	}
	var repos []string
	if raw, ok := obj["repos"]; ok {
		if err := json.Unmarshal(raw, &repos); err != nil {
			return "", fmt.Errorf("%s: repos: %w", path, err)
		}
	} else {
		keys = append(keys, "repos")
	}
	for _, r := range repos {
		if expandHome(filepath.Clean(r)) == dir {
			return dir, nil // already configured
		}
	}
	raw, err := json.Marshal(append(repos, repo))
	if err != nil {
		return "", err
	}
	obj["repos"] = raw
	if err := writeObject(path, obj, keys); err != nil {
		return "", err
	}
	return dir, nil
}

// readObject parses the config into its raw top-level values plus the
// order the keys appear in, so a rewrite can put them back the way the
// operator wrote them. A missing or empty file reads as an empty object
// — the first repo added writes the config into being.
func readObject(path string) (map[string]json.RawMessage, []string, error) {
	obj := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return obj, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return obj, nil, nil
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	keys, err := objectKeys(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return obj, keys, nil
}

// objectKeys lists a JSON object's keys in source order. Unmarshalling
// into a map loses that order, and rewriting a hand-edited config with
// its keys shuffled is rude.
func objectKeys(data []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected key %v", tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// writeObject renders the object in key order and replaces the file with
// it. Values are re-indented, never re-marshalled, so nested keys keep
// their own order too.
func writeObject(path string, obj map[string]json.RawMessage, keys []string) error {
	var b bytes.Buffer
	if len(keys) == 0 {
		b.WriteString("{}\n")
	} else {
		b.WriteString("{\n")
		for i, k := range keys {
			name, err := json.Marshal(k)
			if err != nil {
				return err
			}
			var val bytes.Buffer
			if err := json.Indent(&val, obj[k], "  ", "  "); err != nil {
				return err
			}
			fmt.Fprintf(&b, "  %s: %s", name, val.String())
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString("}\n")
	}
	return writeAtomic(path, b.Bytes())
}

// writeAtomic replaces path in one rename, so an interrupted write
// cannot leave a half-written config behind.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once the rename has succeeded
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SplitCommand splits a tmux command line from the config's "tmux"
// list into argv words, honouring single and double quotes (a quoted
// span keeps its spaces; the quotes themselves are dropped). Errors on
// an empty command or an unclosed quote.
func SplitCommand(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote in %q", quote, s)
	}
	if inWord {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("empty tmux command")
	}
	return words, nil
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
