// internal/toolbox/engine_test.go
package toolbox

import "testing"

func TestFakeEngineRecords(t *testing.T) {
	e := newFakeEngine()
	e.outputs["image inspect coop-tools:base"] = "sha256:abc"
	out, err := e.Output("image", "inspect", "coop-tools:base")
	if err != nil {
		t.Fatal(err)
	}
	if out != "sha256:abc" {
		t.Errorf("out = %q", out)
	}
	if !e.called("image", "inspect") {
		t.Errorf("call not recorded: %s", e.lastCall())
	}
}

func TestNewEngineDefaults(t *testing.T) {
	if got := NewEngine("").Bin; got != "docker" {
		t.Errorf("Bin = %q, want docker", got)
	}
	if got := NewEngine("podman").Bin; got != "podman" {
		t.Errorf("Bin = %q, want podman", got)
	}
}
