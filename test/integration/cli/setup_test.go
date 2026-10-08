//go:build (linux || darwin) && integration

package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

type setupBindingJSON struct {
	Key      string   `json:"key"`
	Table    string   `json:"table"`
	Action   string   `json:"action"`
	Purpose  string   `json:"purpose"`
	Program  []string `json:"program"`
	Status   string   `json:"status"`
	Skipped  bool     `json:"skipped"`
	Conflict bool     `json:"conflict"`
}

type setupApplyResultJSON struct {
	Applied  int                `json:"applied"`
	Skipped  int                `json:"skipped"`
	Bindings []setupBindingJSON `json:"bindings"`
}

func boundKeys(t *testing.T, server *gotmux.Server, table string) map[string]string {
	t.Helper()

	bindings, err := server.FindBindings(t.Context(), gotmux.BindingsOptions{Table: gotmux.KeyTable(table)})
	if err != nil {
		t.Fatalf("list bindings failed: %v", err)
	}

	keys := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		keys[string(binding.Key)] = binding.Raw()
	}

	return keys
}

func TestSetupTmuxApplyRegistersBindings(t *testing.T) {
	server := tmuxtest.NewServer(t)
	socketPath := server.Endpoint().SocketPath

	isolateAutosaveEnv(t)

	exec := executeJSON(t, "setup", "tmux", "--apply", testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)
	if !exec.envelope.OK {
		t.Fatalf("setup tmux --apply failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[setupApplyResultJSON](t, exec.envelope.Result)
	if result.Applied != 4 || result.Skipped != 0 {
		t.Fatalf("applied/skipped = %d/%d, want 4/0", result.Applied, result.Skipped)
	}

	assertBound(t, server, []string{"S-F12", "M-S-F12", "S-F1", "C-o"})

	if got := boundKeys(t, server, "root")["S-F12"]; got == "" {
		t.Fatal("S-F12 binding has an empty payload")
	}
}

func TestSetupTmuxApplySkipsExistingAndForceOverwrites(t *testing.T) {
	server := tmuxtest.NewServer(t)
	socketPath := server.Endpoint().SocketPath

	isolateAutosaveEnv(t)

	first := executeJSON(t, "setup", "tmux", "--apply", testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)
	if !first.envelope.OK {
		t.Fatalf("first apply failed: %+v\nstderr: %s", first.err, first.stderr)
	}

	second := executeJSON(t, "setup", "tmux", "--apply", testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)
	if second.err == nil {
		t.Fatal("second apply must report key conflicts, got nil error")
	}

	if ExitCode(second.err) != exitCodeGeneral {
		t.Fatalf("conflict exit code = %d, want %d", ExitCode(second.err), exitCodeGeneral)
	}

	assertDegradedConflictEnvelope(t, second)

	forced := executeJSON(t, "setup", "tmux", "--apply", "--force", testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)
	if !forced.envelope.OK {
		t.Fatalf("forced apply failed: %+v\nstderr: %s", forced.err, forced.stderr)
	}

	forcedResult := decodeJSONPayload[setupApplyResultJSON](t, forced.envelope.Result)
	if forcedResult.Applied != 4 || forcedResult.Skipped != 0 {
		t.Fatalf("forced applied/skipped = %d/%d, want 4/0", forcedResult.Applied, forcedResult.Skipped)
	}

	assertBound(t, server, []string{"S-F12", "M-S-F12", "S-F1", "C-o"})
}

func TestSetupTmuxApplySkipsAKeyBoundUnderAnotherSpelling(t *testing.T) {
	server := tmuxtest.NewServer(t)
	socketPath := server.Endpoint().SocketPath

	isolateAutosaveEnv(t)

	first := executeJSON(t, "setup", "tmux", "--apply", testSocketFlag, socketPath, testConfigFlag, writeSetupBindings(t, "C-M-F1"), testJSONFlag)
	if !first.envelope.OK {
		t.Fatalf("first apply failed: %+v\nstderr: %s", first.err, first.stderr)
	}

	second := executeJSON(t, "setup", "tmux", "--apply", testSocketFlag, socketPath, testConfigFlag, writeSetupBindings(t, "M-C-F1"), testJSONFlag)
	if second.err == nil {
		t.Fatal("apply of M-C-F1 over the bound C-M-F1 must report a conflict, got nil error")
	}

	if result := decodeJSONPayload[setupApplyResultJSON](t, second.envelope.Result); result.Applied != 0 || result.Skipped != 1 {
		t.Fatalf("applied/skipped = %d/%d, want 0/1", result.Applied, result.Skipped)
	}
}

// writeSetupBindings writes a configuration that binds the given tmux keys in the sesh-test
// table and removes the default bindings.
func writeSetupBindings(t *testing.T, keys ...string) string {
	t.Helper()

	var text strings.Builder

	for _, key := range []string{"S-F12", "M-S-F12", "S-F1", "C-o"} {
		fmt.Fprintf(&text, "[[keys.bind]]\nkey = %q\nin = \"tmux\"\n\n", key)
	}

	for _, key := range keys {
		fmt.Fprintf(&text, "[[keys.bind]]\nkey = %q\naction = \"agent.back\"\nin = \"tmux\"\ntable = \"sesh-test\"\n\n", key)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSetupTmuxApplyTargetsOnlyExplicitSocket(t *testing.T) {
	target := tmuxtest.NewServer(t)
	ambient := tmuxtest.NewServer(t)

	isolateAutosaveEnv(t)
	t.Setenv("TMUX", ambient.Endpoint().SocketPath+",123,0")

	exec := executeJSON(
		t, "setup", "tmux", "--apply",
		testSocketFlag, target.Endpoint().SocketPath,
		testConfigFlag, writeSetupBindings(t, "C-F1", "C-F2"), testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup tmux --apply failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	assertBoundInTable(t, target, "sesh-test", []string{"C-F1", "C-F2"})

	if got := boundKeys(t, ambient, "sesh-test"); len(got) != 0 {
		t.Fatalf("ambient server was mutated: %v", got)
	}
}

func TestSetupTmuxRejectsInvalidKeysBeforeMutation(t *testing.T) {
	server := tmuxtest.NewServer(t)
	isolateAutosaveEnv(t)

	exec := executeJSON(
		t, "setup", "tmux", "--apply",
		testSocketFlag, server.Endpoint().SocketPath,
		testConfigFlag, writeSetupBindings(t, "C-F1", "not a key"), testJSONFlag,
	)
	if exec.err == nil || ExitCode(exec.err) != exitCodeGeneral {
		t.Fatalf("error = %v (exit %d), want a configuration failure", exec.err, ExitCode(exec.err))
	}

	if got := boundKeys(t, server, "sesh-test"); len(got) != 0 {
		t.Fatalf("server mutated before validation failure: %v", got)
	}
}

func assertDegradedConflictEnvelope(t *testing.T, exec commandExecution) {
	t.Helper()

	if exec.envelope.Error == nil {
		t.Fatal("degraded envelope must carry an error payload")
	}

	conflict := decodeJSONPayload[setupApplyResultJSON](t, exec.envelope.Result)
	if conflict.Applied != 0 || conflict.Skipped != 4 {
		t.Fatalf("applied/skipped = %d/%d, want 0/4", conflict.Applied, conflict.Skipped)
	}

	conflicted := 0

	for _, binding := range conflict.Bindings {
		if binding.Conflict && binding.Skipped {
			conflicted++
		}
	}

	if conflicted != 4 {
		t.Fatalf("conflicting bindings = %d, want 4", conflicted)
	}
}

func assertBound(t *testing.T, server *gotmux.Server, keys []string) {
	t.Helper()
	assertBoundInTable(t, server, "root", keys)
}

func assertBoundInTable(t *testing.T, server *gotmux.Server, table string, keys []string) {
	t.Helper()

	bound := boundKeys(t, server, table)

	for _, want := range keys {
		if _, ok := bound[want]; !ok {
			t.Fatalf("key %s is not bound; bound keys: %v", want, bound)
		}
	}
}
