package hub

import (
	"fmt"
	"testing"
)

func TestApplyHubStyleSetsFocusAndBorders(t *testing.T) {
	f := &fakeTmux{}
	if err := ApplyHubStyle(f, "roost", "%0"); err != nil {
		t.Fatal(err)
	}
	if f.serverOpts["focus-events"] != "on" {
		t.Fatalf("focus-events = %q, want on", f.serverOpts["focus-events"])
	}
	// The active style starts DIM: the nav holds focus at startup and its
	// focus signal is the lipgloss frame, not tmux's border. The TUI
	// flips this option to amber only while the preview pane is focused
	// (pane-active-border-style is window-scoped — tmux silently rejects
	// a per-pane set — so it must be driven dynamically).
	want := map[string]string{
		"pane-border-status":       "top",
		"pane-border-style":        "fg=colour" + BorderDim,
		"pane-active-border-style": "fg=colour" + BorderDim,
		// The default 'colour' indicator paints only half of a shared
		// border line to point at the active pane — reads as a broken
		// half-lit split. Off makes active colouring span whole lines.
		"pane-border-indicators": "off",
	}
	for k, v := range want {
		if got := f.windowOpts["roost/"+k]; got != v {
			t.Errorf("window option %s = %q, want %q", k, got, v)
		}
	}
	// The nav pane draws its own framed title — blank tmux's bar for it.
	if got, ok := f.paneOpts["%0/pane-border-format"]; !ok || got != "" {
		t.Errorf("nav pane-border-format = %q (set=%v), want blank", got, ok)
	}
}

// No nav pane id (running outside tmux, tests) skips the per-pane blank
// but still styles the window.
func TestApplyHubStyleWithoutNavPane(t *testing.T) {
	f := &fakeTmux{}
	if err := ApplyHubStyle(f, "roost", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.paneOpts) != 0 {
		t.Fatalf("no pane options expected, got %v", f.paneOpts)
	}
	if f.windowOpts["roost/pane-border-status"] != "top" {
		t.Fatal("window styling should still apply without a nav pane")
	}
}

func TestApplyHubStyleSurfacesError(t *testing.T) {
	f := &fakeTmux{err: fmt.Errorf("tmux exploded")}
	if err := ApplyHubStyle(f, "roost", "%0"); err == nil {
		t.Fatal("tmux error should propagate")
	}
}
