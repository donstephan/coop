package main

import "testing"

func TestIsArbiterCmd(t *testing.T) {
	for _, args := range [][]string{{"peek", "alpha"}, {"answer"}, {"note", "a", "b"}} {
		if !isArbiterCmd(args) {
			t.Errorf("isArbiterCmd(%v) = false", args)
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
