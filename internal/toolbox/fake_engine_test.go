// internal/toolbox/fake_engine_test.go
package toolbox

import (
	"fmt"
	"strings"
)

// fakeEngine stands in for the docker CLI. Unit tests never run a real
// container engine — the same contract fakeTmux has in internal/hub.
type fakeEngine struct {
	calls   [][]string
	outputs map[string]string
	errs    map[string]error
	bin     string
	binErr  error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		outputs: map[string]string{},
		errs:    map[string]error{},
		bin:     "/usr/bin/docker",
	}
}

func (f *fakeEngine) Binary() (string, error) {
	if f.binErr != nil {
		return "", f.binErr
	}
	return f.bin, nil
}

func (f *fakeEngine) Output(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args, " ")
	if err, ok := f.errs[key]; ok {
		return "", err
	}
	if out, ok := f.outputs[key]; ok {
		return out, nil
	}
	return "", nil
}

func (f *fakeEngine) Run(args ...string) error {
	_, err := f.Output(args...)
	return err
}

// called reports whether any recorded call starts with prefix.
func (f *fakeEngine) called(prefix ...string) bool {
	for _, c := range f.calls {
		if len(c) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c[i] != p {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// lastCall renders the most recent call, for failure messages.
func (f *fakeEngine) lastCall() string {
	if len(f.calls) == 0 {
		return "<none>"
	}
	return fmt.Sprint(f.calls[len(f.calls)-1])
}
