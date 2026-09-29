package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/gotmux/tmuxtest"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
)

const (
	testConfigPathFlag  = "--path"
	contractStatusField = "status"
	contractReasonField = "reason"
)

const (
	testListCmd    = "list"
	testHistoryCmd = "history"
)

func TestJSONCommandTreeRequiresExplicitOutputContract(t *testing.T) {
	contracts := map[string][]string{
		"envelope": {
			"action agent-back", "action agent-focus", "action agent-next", "action agent-status", "action agent-status clear",
			"action autosave-restore-on-start", "action autosave-save-if-due", "action autosave-status",
			"action session-delete", "action session-kill", "action session-new", "action session-rename", "action session-switch",
			"action snapshot-restore", "action snapshot-save",
			"autosave ensure-default-session", "autosave final-save",
			"config init", "doctor", testHistoryCmd, "hooks", "hooks run", "keys", testListCmd,
			"remote action", "remote lookup", "setup agents", "setup autosave", "setup coordinator", "setup refresh", "setup remote", "setup tmux",
			"snapshot abandon", "snapshot gc", "snapshot history", "snapshot list", "snapshot prune", "snapshot reconcile",
			"snapshot rewrite reconcile", "snapshot rewrite resume", "snapshot verify",
		},
		"log stream; path and clear use envelopes": {"logs"},
		"interactive terminal":                     {"action picker-open", "pick", "remote attach"},
		"foreground service or readiness protocol": {
			"action agent-status run", "daemon run", "daemon serve", "daemon wait-ready", "remote monitor run",
		},
		"private peer protocol": {"remote peer action", "remote peer attach", "remote peer attach-prepare", "remote peer lookup", "remote peer stream"},
		"plain help or version": {"", actionCmd, "autosave", "config", "daemon", "help", "remote", "remote monitor", "remote peer", "setup", snapshotCmd, "snapshot rewrite"},
		"shell completion":      {"completion", "completion bash", "completion fish", "completion powershell", "completion zsh"},
	}
	want := make(map[string]bool)

	for kind, paths := range contracts {
		for _, path := range paths {
			if _, duplicate := want[path]; duplicate {
				t.Fatalf("duplicate command contract: %q", path)
			}

			want[path] = kind == "envelope" || kind == "log stream; path and clear use envelopes"
		}
	}

	root := newRootCommand(t.Context())
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	got := make(map[string]bool)
	collectJSONCommands(root, got)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("command output contract changed; explicitly classify new or changed commands (-want +got):\n%s", diff)
	}
}

func collectJSONCommands(cmd *cobra.Command, commands map[string]bool) {
	path := strings.TrimPrefix(strings.TrimPrefix(cmd.CommandPath(), "sesh"), " ")
	commands[path] = cmd.Flags().Lookup("json") != nil

	for _, child := range cmd.Commands() {
		collectJSONCommands(child, commands)
	}
}

func TestJSONCommandsRejectUnknownFlagsWithoutHumanOutput(t *testing.T) {
	root := newRootCommand(t.Context())
	commands := make(map[string]bool)
	collectJSONCommands(root, commands)

	for path, supportsJSON := range commands {
		if !supportsJSON {
			continue
		}

		t.Run(path, func(t *testing.T) {
			args := append(strings.Fields(path), testJSONFlag, "--contract-unknown-flag")
			execution := executeJSON(t, args...)
			assertErrorEnvelope(t, execution, "usageError", 2, "unknown flag")
			fields := contractJSONObject(t, []byte(execution.stderr))
			assertJSONContractFields(t, fields, map[string]any{
				"schemaVersion": float64(1), "command": strings.ReplaceAll(path, " ", "."), "ok": false, "result": nil,
			})
		})
	}
}

func isolateJSONContractHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()

	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, home)
	}

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	return home
}

func runJSONContract(t *testing.T, command string, args ...string) map[string]any {
	t.Helper()
	execution := executeJSON(t, append(args, testNoConfigFlag, testJSONFlag)...)

	return assertJSONContractSuccess(t, command, execution)
}

func assertJSONContractSuccess(t *testing.T, command string, execution commandExecution) map[string]any {
	t.Helper()

	if execution.err != nil || execution.stderr != "" {
		t.Fatalf("%s: error=%v, stderr=%q, stdout=%q", command, execution.err, execution.stderr, execution.stdout)
	}

	fields := contractJSONObject(t, []byte(execution.stdout))
	if len(fields) != 5 {
		t.Fatalf("envelope must contain schemaVersion, command, ok, result, error: %v", fields)
	}

	assertJSONContractFields(t, fields, map[string]any{
		"schemaVersion": float64(1), "command": command, "ok": true, "error": nil,
	})
	assertAllKeysCamelCase(t, fields, "$")

	return contractJSONValue[map[string]any](t, fields, "result")
}

func contractJSONObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		t.Fatalf("expected one JSON object: %s, error=%v", raw, err)
	}

	return object
}

func contractJSONValue[T any](t *testing.T, object map[string]any, key string) T {
	t.Helper()

	value, ok := object[key].(T)
	if !ok {
		t.Fatalf("%s has missing/wrong JSON type: %#v", key, object[key])
	}

	return value
}

func assertJSONContractFields(t *testing.T, object, want map[string]any) {
	t.Helper()

	for key, expected := range want {
		actual, present := object[key]
		if !present {
			t.Errorf("required JSON field %q is absent: %v", key, object)
		} else if diff := cmp.Diff(expected, actual); diff != "" {
			t.Errorf("JSON field %q (-want +got):\n%s", key, diff)
		}
	}
}

func TestJSONOfflineCommandSuccessContracts(t *testing.T) {
	home := isolateJSONContractHome(t)

	cases := []struct {
		command string
		args    []string
		want    map[string]any
	}{
		{command: "config.init", args: []string{"config", "init", testConfigPathFlag, filepath.Join(home, "native.toml")}, want: map[string]any{"path": filepath.Join(home, "native.toml"), contractStatusField: "initialized"}},
		{command: "action.agent-status", args: []string{actionCmd, "agent-status", "--state", filepath.Join(home, "missing-agents.json")}, want: map[string]any{"total": float64(0), "live": float64(0), "waiting": float64(0), "failed": float64(0), "unread": float64(0), "idle": float64(0)}},
		{command: "action.autosave-status", args: []string{actionCmd, "autosave-status"}, want: map[string]any{contractStatusField: "due", contractReasonField: "never_run", "recorded": false, "elapsedSeconds": float64(0), "intervalMinutes": float64(3), "historyState": "never_run", "repositoryState": "missing", "inhibitorsHeld": false}},
		{command: "setup.tmux", args: []string{"setup", "tmux"}, want: map[string]any{"table": "root", "applied": float64(0), "skipped": float64(0)}},
	}
	for _, test := range cases {
		t.Run(test.command, func(t *testing.T) {
			result := runJSONContract(t, test.command, test.args...)
			assertJSONContractFields(t, result, test.want)
		})
	}
}

func TestJSONKeysAndHooksContainConfiguredValues(t *testing.T) {
	isolateJSONContractHome(t)
	keys := runJSONContract(t, "keys", "keys")

	bindings := contractJSONValue[[]any](t, keys, "bindings")
	if len(bindings) == 0 {
		t.Fatal("default key bindings absent")
	}

	create := contractJSONItem(t, bindings, "name", "create")
	assertJSONContractFields(t, create, map[string]any{"name": "create", "bind": "alt+c"})
	hooks := runJSONContract(t, "hooks", "hooks")
	_ = contractJSONValue[[]any](t, hooks, "events")
}

func TestJSONServiceSetupPreviewContracts(t *testing.T) {
	home := isolateJSONContractHome(t)
	for _, command := range []string{"autosave", "coordinator", "remote", "refresh"} {
		t.Run(command, func(t *testing.T) {
			result := runJSONContract(t, "setup."+command, "setup", command, "--dir", filepath.Join(home, command))
			assertJSONContractFields(t, result, map[string]any{"applied": false, "written": float64(0)})
			_ = contractJSONValue[string](t, result, "platform")

			services := contractJSONValue[[]any](t, result, "services")
			if len(services) == 0 {
				t.Fatal("service preview is empty")
			}

			service, ok := services[0].(map[string]any)
			if !ok {
				t.Fatalf("service must be an object: %v", services[0])
			}

			_ = contractJSONValue[string](t, service, "name")
			_ = contractJSONValue[string](t, service, "content")
			assertJSONContractFields(t, service, map[string]any{"state": "missing", "changed": true})

			if _, err := os.Stat(filepath.Join(home, command)); !os.IsNotExist(err) {
				t.Fatalf("preview created definition directory: %v", err)
			}
		})
	}
}

func contractJSONItem(t *testing.T, values []any, key string, want any) map[string]any {
	t.Helper()

	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("array item is not a JSON object: %v", value)
		}

		if cmp.Equal(object[key], want) {
			return object
		}
	}

	t.Fatalf("no JSON array item has %s=%v: %v", key, want, values)

	return nil
}

func TestJSONDoctorWarningReportSuccessContract(t *testing.T) {
	home := isolateJSONContractHome(t)
	socket := tmuxtest.NewServer(t).Endpoint().SocketPath
	result := runJSONContract(t, "doctor", "doctor", testSocketFlag, socket, testRepositoryFlag, filepath.Join(home, "missing-repo"), "--history", filepath.Join(home, "missing-history"))
	assertJSONContractFields(t, result, map[string]any{"hasErrors": false, "hasWarnings": true})

	checks := contractJSONValue[[]any](t, result, "checks")
	if len(checks) == 0 {
		t.Fatal("doctor omitted its checks")
	}
}

func TestJSONAgentSetupPreviewContract(t *testing.T) {
	home := isolateJSONContractHome(t)
	result := runJSONContract(t, "setup.agents", "setup", "agents", "codex", "--dry-run", "--tracker=")
	assertJSONContractFields(t, result, map[string]any{"dryRun": true})
	harnesses := contractJSONValue[[]any](t, result, "harnesses")
	codex := contractJSONItem(t, harnesses, "harness", "codex")
	_ = contractJSONValue[bool](t, codex, "changed")
	tracker := contractJSONValue[map[string]any](t, result, "tracker")
	assertJSONContractFields(t, tracker, map[string]any{"installed": false, "running": false, "changed": false, "current": false})

	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("preview created harness configuration: %v", err)
	}
}

func TestJSONDisabledAutosaveSuccessContracts(t *testing.T) {
	home := isolateJSONContractHome(t)

	configPath := filepath.Join(home, "disabled.toml")
	if err := os.WriteFile(configPath, []byte("[autosave]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	execution := executeJSON(t, actionCmd, "autosave-save-if-due", testConfigFlag, configPath, testJSONFlag)
	result := assertJSONContractSuccess(t, "action.autosave-save-if-due", execution)
	assertJSONContractFields(t, result, map[string]any{contractStatusField: "disabled", contractReasonField: "disabled", "recorded": false, "inhibitorsHeld": false})

	if _, present := result["generation"]; present {
		t.Fatal("disabled autosave claims a saved generation")
	}
}
