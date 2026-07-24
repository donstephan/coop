// Package config reads the coop config file: the repos the TUI can
// create Claude Code sessions from.
package config

import (
	"encoding/json"
	"fmt"
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
