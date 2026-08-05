// internal/toolbox/engine.go
package toolbox

import (
	"fmt"
	"os/exec"
	"strings"
)

// Engine is the subset of the container CLI the toolbox needs — the same
// shape as hub.Tmux, and for the same reason: unit tests substitute a
// fake and never start a container.
type Engine interface {
	// Binary resolves the engine's executable, for callers that exec it
	// directly rather than through this interface.
	Binary() (string, error)
	// Output runs the engine and returns its combined output.
	Output(args ...string) (string, error)
	// Run runs the engine and discards its output.
	Run(args ...string) error
}

// ExecEngine shells out to docker (or podman).
type ExecEngine struct{ Bin string }

// NewEngine returns an ExecEngine for the configured engine name.
func NewEngine(name string) *ExecEngine {
	if name == "" {
		name = "docker"
	}
	return &ExecEngine{Bin: name}
}

func (e *ExecEngine) Binary() (string, error) { return exec.LookPath(e.Bin) }

func (e *ExecEngine) Output(args ...string) (string, error) {
	out, err := exec.Command(e.Bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", e.Bin,
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (e *ExecEngine) Run(args ...string) error {
	_, err := e.Output(args...)
	return err
}
