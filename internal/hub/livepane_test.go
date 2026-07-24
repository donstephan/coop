package hub

import (
	"strings"
	"testing"
)

func TestAttachCmdShape(t *testing.T) {
	cmd := AttachCmd("cc", "dataset-v2")
	for _, want := range []string{
		"TMUX=",                   // clear TMUX or tmux refuses to nest
		"tmux -L 'cc'",            // same socket
		"attach -t '=dataset-v2'", // exact-match target, no prefix surprises
		"-f ignore-size",          // never fight the Ghostty client for size
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("AttachCmd missing %q: %s", want, cmd)
		}
	}
}

func TestAttachCmdQuotesSingleQuotes(t *testing.T) {
	cmd := AttachCmd("cc", "don's-run")
	if !strings.Contains(cmd, `'=don'\''s-run'`) {
		t.Errorf("session name not shell-quoted: %s", cmd)
	}
}

func TestPlaceholderCmdNeverExits(t *testing.T) {
	cmd := PlaceholderCmd()
	if !strings.Contains(cmd, "no session selected") {
		t.Errorf("placeholder missing message: %s", cmd)
	}
	if !strings.Contains(cmd, "sleep infinity") {
		// It must stay alive: remain-on-exit is set after split, so an
		// instantly-exiting command could die before the option lands.
		t.Errorf("placeholder must block forever: %s", cmd)
	}
}

func TestParseClientFlags(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"", false},   // no clients at all
		{"\n", false}, // blank line from trailing newline
		{"attached,focused,ignore-size,UTF-8\n", false},        // preview client only
		{"attached,focused,UTF-8\n", true},                     // real client
		{"attached,ignore-size,UTF-8\nattached,UTF-8\n", true}, // one of each
	}
	for _, c := range cases {
		if got := parseClientFlags(c.out); got != c.want {
			t.Errorf("parseClientFlags(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

func TestParseMarkedPane(t *testing.T) {
	out := "%1\x1f\n%5\x1f1\n%9\x1f\n"
	if got := parseMarkedPane(out); got != "%5" {
		t.Errorf("want %%5, got %q", got)
	}
	if got := parseMarkedPane("%1\x1f\n"); got != "" {
		t.Errorf("no marked pane should give empty, got %q", got)
	}
}
