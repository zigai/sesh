package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samber/oops"
)

func TestRootCommandShowsHelpByDefault(t *testing.T) {
	stdout, stderr, err := executeCommand(t)
	if err != nil {
		t.Fatalf("execute root command: %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if !strings.Contains(stdout, "Usage:") {
		t.Fatalf("stdout = %q, want Usage section", stdout)
	}

	if !strings.Contains(stdout, "sesh [flags]") {
		t.Fatalf("stdout = %q, want command usage", stdout)
	}
}

func TestRootCommandShowsVersion(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "--version")
	if err != nil {
		t.Fatalf("execute root command: %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	want := "sesh dev (commit: none, built: unknown)\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestRootHelpOrdersCoreCommandsFirstWithDoctorBeforeHelp(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "--help")
	if err != nil || stderr != "" {
		t.Fatalf("help failed: %v, stderr %q", err, stderr)
	}

	section, found := strings.CutPrefix(stdout, "Native tmux session persistence\n\n")
	if !found {
		t.Fatalf("unexpected root help: %s", stdout)
	}

	_, section, found = strings.Cut(section, "Available Commands:\n")
	if !found {
		t.Fatalf("root help has no command list: %s", stdout)
	}

	section, _, found = strings.Cut(section, "\n\nFlags:")
	if !found {
		t.Fatalf("root help has no flags after commands: %s", stdout)
	}

	var names []string

	for line := range strings.SplitSeq(section, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}

	want := []string{
		"pick", "session", "snapshot", "agent", "autosave", "remote",
		"keys", "logs", "hooks", "config", "setup", "daemon", "doctor", "help",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("command order = %v, want %v", names, want)
	}

	if strings.Contains(stdout, "Core Commands:") || strings.Contains(stdout, "Management Commands:") {
		t.Fatalf("help split commands into categories: %s", stdout)
	}
}

func TestHelpAndVersionShorthands(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {snapshotCmd, "-h"}, {"logs", "-h"}} {
		stdout, _, err := executeCommand(t, args...)
		if ExitCode(err) != exitCodeUsage || stdout != "" {
			t.Fatalf("%v returned code %d and stdout %q", args, ExitCode(err), stdout)
		}
	}

	stdout, stderr, err := executeCommand(t, "-V")
	if err != nil || stderr != "" || !strings.HasPrefix(stdout, "sesh dev") {
		t.Fatalf("-V returned %q, %q, %v", stdout, stderr, err)
	}
}

func TestVersionOutputFailureRetainsDiagnostics(t *testing.T) {
	cause := &os.PathError{Op: "write", Path: "/private/synthetic-output", Err: os.ErrPermission}

	var stderr bytes.Buffer

	err := ExecuteApplication(t.Context(), []string{"--version"}, nil, failedWriter{cause: cause}, &stderr)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatal("lost permission identity")
	}

	pathErr, ok := errors.AsType[*os.PathError](err)
	if !ok || pathErr != cause {
		t.Fatal("lost typed output failure")
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok || diagnostic.Domain() != "cli" || diagnostic.Context()["operation"] != "write_output" {
		t.Fatal("missing command diagnostics")
	}

	if diagnostic.Stacktrace() == "" {
		t.Fatal("missing diagnostic stack")
	}

	if err.Error() != "writing command output: "+cause.Error() {
		t.Fatal("changed command error message")
	}
}

type failedWriter struct{ cause error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.cause }

func isolatedToolsHome(t *testing.T) {
	t.Helper()

	t.Setenv("TMUX", "")

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
}

const hooksTestConfig = `[hooks]
timeout = "5s"

[[hooks.session_switch_pre]]
command = "true"

[[hooks.session_switch_pre]]
command = "printf"
args = ["hook-ran"]
`

func writeToolsConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestHooksCommandListsConfiguredEvents(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, hooksTestConfig)

	exec := executeJSON(t, "hooks", "list", testConfigFlag, cfgPath, testJSONFlag)
	if !exec.envelope.OK {
		t.Fatalf("hooks failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[hooksResultJSON](t, exec.envelope.Result)
	if result.TimeoutSeconds != 5 {
		t.Fatalf("timeoutSeconds = %d, want 5", result.TimeoutSeconds)
	}

	counts := make(map[string]int, len(result.Events))
	for _, event := range result.Events {
		counts[event.Event] = event.Commands
	}

	if counts["session_switch_pre"] != 2 {
		t.Fatalf("session_switch_pre commands = %d, want 2", counts["session_switch_pre"])
	}

	if counts["session_switch_post"] != 0 {
		t.Fatalf("session_switch_post commands = %d, want 0", counts["session_switch_post"])
	}

	if len(result.Events) != 5 {
		t.Fatalf("events = %d, want 5", len(result.Events))
	}
}

func TestHooksCommandValidateAcceptsValidConfig(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, hooksTestConfig)

	stdout, stderr, err := executeCommand(t, "hooks", "validate", testConfigFlag, cfgPath)
	if err != nil {
		t.Fatalf("hooks --validate failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "session_switch_pre") {
		t.Fatalf("stdout missing event summary: %s", stdout)
	}
}

func TestHooksCommandValidateRejectsUnsupportedShellMode(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, `[hooks]

[[hooks.session_switch_pre]]
shell = "exit 0"
`)

	_, _, err := executeCommand(t, "hooks", "validate", testConfigFlag, cfgPath)
	if err == nil {
		t.Fatal("expected validation failure for an unsupported hook entry")
	}
}

func TestHooksRunRequiresConfiguredEvent(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, hooksTestConfig)

	_, _, err := executeCommand(t, "hooks", "run", "session_attach_pre", testConfigFlag, cfgPath)
	if err == nil {
		t.Fatal("expected failure for an event with no configured hooks")
	}

	if ExitCode(err) != exitCodeGeneral {
		t.Fatalf("exit code = %d, want %d", ExitCode(err), exitCodeGeneral)
	}
}

func TestHooksRunRejectsUnknownEvent(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, hooksTestConfig)

	_, _, err := executeCommand(t, "hooks", "run", "not_an_event", testConfigFlag, cfgPath)
	if err == nil {
		t.Fatal("expected usage failure for an unknown event")
	}

	if ExitCode(err) != exitCodeUsage {
		t.Fatalf("exit code = %d, want %d", ExitCode(err), exitCodeUsage)
	}
}

func TestHooksRunExecutesConfiguredHooks(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, hooksTestConfig)

	exec := executeJSON(
		t, "hooks", "run", "session_switch_pre",
		"--session", "demo",
		testConfigFlag, cfgPath, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("hooks run failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[hookRunResultJSON](t, exec.envelope.Result)
	if result.Attempted != 2 || result.Completed != 2 || result.Failed != 0 {
		t.Fatalf("result = %#v, want 2 attempted/completed and 0 failed", result)
	}

	if result.Status != "ok" {
		t.Fatalf("status = %q, want ok", result.Status)
	}
}

func TestHooksRunReportsDegradedOutcome(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, `[hooks]

[[hooks.session_switch_pre]]
command = "true"

[[hooks.session_switch_pre]]
command = "false"
`)

	exec := executeJSON(t, "hooks", "run", "session_switch_pre", testConfigFlag, cfgPath, testJSONFlag)
	if exec.err == nil {
		t.Fatal("expected a failing hook to surface an error")
	}

	if ExitCode(exec.err) != exitCodeGeneral {
		t.Fatalf("exit code = %d, want %d", ExitCode(exec.err), exitCodeGeneral)
	}

	result := decodeJSONPayload[hookRunResultJSON](t, exec.envelope.Result)
	if result.Failed != 1 || result.Completed != 1 {
		t.Fatalf("result = %#v, want 1 failed and 1 completed", result)
	}

	if result.Status != "degraded" {
		t.Fatalf("status = %q, want degraded", result.Status)
	}
}

func TestHooksRunPlainDegradedKeepsCounts(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, `[hooks]

[[hooks.session_switch_pre]]
command = "false"
`)

	stdout, stderr, err := executeCommand(t, "hooks", "run", "session_switch_pre", testConfigFlag, cfgPath)
	if err == nil {
		t.Fatal("expected a failing hook to surface an error")
	}

	if !strings.Contains(stderr, "attempted 1, completed 0, failed 1") {
		t.Fatalf("stderr missing degraded counts: %q", stderr)
	}

	if stdout != "" {
		t.Fatalf("stdout must stay empty on a degraded run, got %q", stdout)
	}
}

func TestKeysCommandListsBindings(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, `[[keys.bind]]
key = "ctrl+g"
action = "session.create"
in = "picker"

[[keys.bind]]
key = "alt+c"
in = "picker"

[[keys.bind]]
key = "alt+s"
in = "picker"
`)

	exec := executeJSON(t, "keys", testConfigFlag, cfgPath, testJSONFlag)
	if !exec.envelope.OK {
		t.Fatalf("keys failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	payload := decodeJSONPayload[struct {
		Bindings []keyBindingJSON `json:"bindings"`
	}](t, exec.envelope.Result)

	byName := make(map[string]string, len(payload.Bindings))
	for _, binding := range payload.Bindings {
		byName[binding.Name] = binding.Bind
	}

	if byName["session.create"] != "ctrl+g" {
		t.Fatalf("session.create binding = %q, want ctrl+g", byName["session.create"])
	}

	if bind, ok := byName["snapshot.save"]; ok {
		t.Fatalf("unbound snapshot.save is still bound to %q", bind)
	}

	if len(payload.Bindings) != 21 {
		t.Fatalf("bindings = %d, want 21", len(payload.Bindings))
	}
}

func TestKeysCommandPlainOutput(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, `[keys]
help = "f1"
`)

	stdout, stderr, err := executeCommand(t, "keys", testConfigFlag, cfgPath)
	if err != nil {
		t.Fatalf("keys failed: %v\nstderr: %s", err, stderr)
	}

	for _, want := range [][]string{{"help", "f1"}, {"session.kill", "alt+x"}} {
		if !slices.ContainsFunc(strings.Split(stdout, "\n"), func(line string) bool { return slices.Equal(strings.Fields(line), want) }) {
			t.Fatalf("stdout missing %s binding:\n%s", want[0], stdout)
		}
	}
}
