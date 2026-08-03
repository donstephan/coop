package main

import "testing"

func TestIsArbiterCmdOnlyPeek(t *testing.T) {
	if !isArbiterCmd([]string{"peek", "alpha"}) {
		t.Error("peek should still dispatch")
	}
	for _, verb := range []string{"answer", "note"} {
		if isArbiterCmd([]string{verb, "alpha"}) {
			t.Errorf("%s is an internal call now, not a CLI verb", verb)
		}
	}
	for _, args := range [][]string{{}, {"-socket", "x"}, {"help"}} {
		if isArbiterCmd(args) {
			t.Errorf("isArbiterCmd(%v) = true", args)
		}
	}
}

func TestCliSocket(t *testing.T) {
	t.Setenv("COOP_SOCKET", "")
	t.Setenv("TMUX", "/tmp/tmux-1000/coop-e2e,1234,0")
	if got := cliSocket(); got != "coop-e2e" {
		t.Errorf("from TMUX: %q", got)
	}
	t.Setenv("COOP_SOCKET", "override")
	if got := cliSocket(); got != "override" {
		t.Errorf("COOP_SOCKET wins: %q", got)
	}
	t.Setenv("COOP_SOCKET", "")
	t.Setenv("TMUX", "")
	if got := cliSocket(); got != "coop" {
		t.Errorf("default: %q", got)
	}
}
