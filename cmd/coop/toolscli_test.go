package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewToolboxCmdRejectsBadIdle(t *testing.T) {
	// Caught at generation, where the answer is still "no shims and an
	// untouched PATH". Left to the first exec it would be every shimmed
	// command in every live session failing on one bad line.
	fn, err := newToolboxCmd(writeConfig(t, `{"toolbox":{"idle_timeout":"soon"}}`))
	if err == nil {
		t.Fatal("an unparseable idle_timeout must fail generation")
	}
	if fn != nil {
		t.Error("a failed generation must not return a PATH rewriter")
	}
}

func TestNewToolboxCmdDisabled(t *testing.T) {
	fn, err := newToolboxCmd(writeConfig(t, `{"toolbox":{"enabled":false}}`))
	if err != nil {
		t.Fatalf("disabled toolbox should not error: %v", err)
	}
	if fn != nil {
		t.Error("a disabled toolbox must not rewrite PATH")
	}
}

func TestIsToolsCmd(t *testing.T) {
	if !isToolsCmd([]string{"tools", "ls"}) {
		t.Error("tools ls should dispatch")
	}
	if isToolsCmd([]string{"-socket", "coop"}) {
		t.Error("flags must not dispatch")
	}
	if isToolsCmd(nil) {
		t.Error("no args must not dispatch")
	}
}

func TestToolsCLIUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runToolsCLI([]string{"tools"}, &out, &errb); code == 0 {
		t.Error("bare 'tools' should be a usage error")
	}
	if !strings.Contains(errb.String(), "exec") {
		t.Errorf("usage should list subcommands: %q", errb.String())
	}
}

func TestToolsCLIUnknownSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runToolsCLI([]string{"tools", "frobnicate"}, &out, &errb); code == 0 {
		t.Error("unknown subcommand should fail")
	}
}

func TestToolsExecNeedsArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runToolsCLI([]string{"tools", "exec", "/home/user/sprocket-v2"}, &out, &errb); code == 0 {
		t.Error("exec without a tool should fail")
	}
}
