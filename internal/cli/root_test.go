package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samber/oops"
)

func TestRootCommandShowsHelpByDefault(t *testing.T) {
	stdout, stderr, err := executeRootCommand()
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
	stdout, stderr, err := executeRootCommand("--version")
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
	stdout, stderr, err := executeRootCommand("--help")
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
		"pick", "list", "snapshot", "autosave", "history", "remote",
		"keys", "logs", "hooks", "config", "setup", "daemon", "action", "doctor", "help",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("command order = %v, want %v", names, want)
	}

	if strings.Contains(stdout, "Core Commands:") || strings.Contains(stdout, "Management Commands:") {
		t.Fatalf("help split commands into categories: %s", stdout)
	}
}

func TestHelpAndVersionShorthands(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"snapshot", "-h"}, {"logs", "-h"}} {
		stdout, _, err := executeRootCommand(args...)
		if ExitCode(err) != exitCodeUsage || stdout != "" {
			t.Fatalf("%v returned code %d and stdout %q", args, ExitCode(err), stdout)
		}
	}

	stdout, stderr, err := executeRootCommand("-V")
	if err != nil || stderr != "" || !strings.HasPrefix(stdout, "sesh dev") {
		t.Fatalf("-V returned %q, %q, %v", stdout, stderr, err)
	}
}

func TestVersionOutputFailureRetainsDiagnostics(t *testing.T) {
	cause := &os.PathError{Op: "write", Path: "/private/synthetic-output", Err: os.ErrPermission}
	command := newRootCommand(t.Context())
	command.SetOut(failedWriter{cause: cause})

	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{"--version"})

	err := command.Execute()
	if !errors.Is(err, os.ErrPermission) {
		t.Fatal("lost permission identity")
	}

	pathErr, ok := errors.AsType[*os.PathError](err)
	if !ok || pathErr != cause {
		t.Fatal("lost typed output failure")
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok || diagnostic.Domain() != "cli" || diagnostic.Context()["operation"] != "print_version" {
		t.Fatal("missing command diagnostics")
	}

	if diagnostic.Stacktrace() == "" {
		t.Fatal("missing diagnostic stack")
	}

	if err.Error() != "print version: "+cause.Error() {
		t.Fatal("changed command error message")
	}
}

type failedWriter struct{ cause error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.cause }

func executeRootCommand(args ...string) (string, string, error) {
	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	rootCommand := newRootCommand(context.Background())
	rootCommand.SetOut(&stdout)
	rootCommand.SetErr(&stderr)
	rootCommand.SetArgs(args)

	err := rootCommand.Execute()

	return stdout.String(), stderr.String(), err
}

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

	exec := executeJSON(t, "hooks", testConfigFlag, cfgPath, testJSONFlag)
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

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "hooks", "--validate", testConfigFlag, cfgPath)
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

	root := newRootCommand(t.Context())

	_, _, err := executeCommand(root, "hooks", "--validate", testConfigFlag, cfgPath)
	if err == nil {
		t.Fatal("expected validation failure for an unsupported hook entry")
	}
}

func TestHooksRunRequiresConfiguredEvent(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, hooksTestConfig)

	root := newRootCommand(t.Context())

	_, _, err := executeCommand(root, "hooks", "run", "session_attach_pre", testConfigFlag, cfgPath)
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

	root := newRootCommand(t.Context())

	_, _, err := executeCommand(root, "hooks", "run", "not_an_event", testConfigFlag, cfgPath)
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

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "hooks", "run", "session_switch_pre", testConfigFlag, cfgPath)
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

	cfgPath := writeToolsConfig(t, `[keys]
create = "alt+c"
snapshot = "alt+s"
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

	if byName["create"] != "alt+c" {
		t.Fatalf("create binding = %q, want alt+c", byName["create"])
	}

	if byName["snapshot"] != "alt+s" {
		t.Fatalf("snapshot binding = %q, want alt+s", byName["snapshot"])
	}

	if len(payload.Bindings) != 22 {
		t.Fatalf("bindings = %d, want 22", len(payload.Bindings))
	}
}

func TestKeysCommandPlainOutput(t *testing.T) {
	isolatedToolsHome(t)

	cfgPath := writeToolsConfig(t, `[keys]
kill = "alt+x"
`)

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "keys", testConfigFlag, cfgPath)
	if err != nil {
		t.Fatalf("keys failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "kill           alt+x") {
		t.Fatalf("stdout missing kill binding:\n%s", stdout)
	}
}
