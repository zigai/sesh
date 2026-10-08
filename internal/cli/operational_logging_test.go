package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
)

func TestOperationalCommandsPersistFailuresWithoutPrivateDetails(t *testing.T) {
	cases := []struct {
		args      []string
		operation string
	}{
		{[]string{"autosave", "final-save"}, "final-save"},
		{[]string{"autosave", "adopt-startup"}, "adopt_startup_session"},
		{[]string{"daemon", "wait-ready"}, "daemon_wait_ready"},
		{[]string{"daemon", "agent-window-status"}, "agent_status_run"},
		{[]string{"agent", "window-status-clear"}, "window-status-clear"},
		{[]string{"setup", "tmux"}, "setup_tmux"},
		{[]string{"setup", "agents"}, "setup_agents"},
		{[]string{"hooks", "run", "session_switch_pre"}, "execute_hooks"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			cfg, path := runtimeConfig(t, "info")

			var stdout, stderr bytes.Buffer

			root := newRootCommand(t.Context())

			cmd, _, err := root.Find(tc.args)
			if err != nil {
				t.Fatal(err)
			}

			cmd.RunE = func(*cobra.Command, []string) error { return errSensitiveRaw }

			root.SetArgs(append(tc.args, testConfigFlag, cfg))
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			runtime := commandRuntime{cmd: root}
			root.PersistentPreRunE = runtime.prepare

			_, runErr := root.ExecuteContextC(t.Context())
			if !errors.Is(runErr, errSensitiveRaw) {
				t.Fatalf("lost failure: %v", runErr)
			}

			if err := runtime.finish(t.Context(), runErr); err != nil {
				t.Fatal(err)
			}

			assertRuntimeLog(t, path, "cli", tc.operation, "error", "failed", cfg, errSensitiveRaw.Error())

			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("logging contaminated command streams")
			}
		})
	}
}

func TestSetupTmuxRenderingPersistsSuccessAndKeepsJSONSeparate(t *testing.T) {
	cfg, path := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer
	if err := ExecuteApplication(t.Context(), []string{"setup", "tmux", testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	if !singleRuntimeEnvelope(t, stdout.Bytes()).OK || stderr.Len() != 0 {
		t.Fatal("setup JSON result contaminated")
	}

	assertRuntimeLog(t, path, "cli", "setup_tmux", "info", "ok", cfg)
}

func TestHooksPersistExecutionWithoutRepeatingSuccessOrLeakingOutput(t *testing.T) {
	base := t.TempDir()
	cfg := filepath.Join(base, "config.toml")
	path := filepath.Join(base, "hook.jsonl")

	data := fmt.Sprintf("[settings]\nlog_level = \"info\"\n[paths]\nlog_file = %q\n[[hooks.session_switch_pre]]\ncommand = \"printf\"\nargs = [\"PRIVATE_HOOK_OUTPUT\"]\n", path)
	if err := os.WriteFile(cfg, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	args := []string{"hooks", "run", "session_switch_pre", "--session", "PRIVATE_SESSION", "--source", "PRIVATE_SOURCE", testConfigFlag, cfg, testJSONFlag}
	if err := ExecuteApplication(t.Context(), args, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	if !singleRuntimeEnvelope(t, stdout.Bytes()).OK || stderr.Len() != 0 {
		t.Fatal("hook logging contaminated the JSON result")
	}

	logs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	assertHookExecutionRecords(t, logs, cfg)
}

func assertHookExecutionRecords(t *testing.T, logs []byte, cfg string) {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(logs), []byte("\n"))
	if len(lines) != 3 || bytes.Contains(logs, []byte("PRIVATE_")) || bytes.Contains(logs, []byte(cfg)) {
		t.Fatalf("unexpected hook records: %s", logs)
	}

	assertRuntimeRecord(t, lines[0], "cli", "execute_hooks", "info", "started")

	for i, status := range []string{"started", "ok"} {
		var record map[string]any
		if err := json.Unmarshal(lines[i+1], &record); err != nil {
			t.Fatal(err)
		}

		if record["component"] != "hooks" || record["operation"] != "execute" || record["level"] != "info" || record["status"] != status {
			t.Fatalf("incorrect hook boundary: %s", lines[i+1])
		}
	}
}

func TestDisabledAutosaveLogsItsReasonAndKeepsJSONSeparate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := t.TempDir()
	cfg := filepath.Join(base, "config.toml")
	path := filepath.Join(base, "log.jsonl")

	data := fmt.Sprintf("[settings]\nlog_level = \"info\"\n[paths]\nlog_file = %q\n[autosave]\nenabled = false\n", path)
	if err := os.WriteFile(cfg, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	args := []string{"autosave", "save-if-due", testConfigFlag, cfg, testJSONFlag}
	if err := ExecuteApplication(t.Context(), args, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	if !singleRuntimeEnvelope(t, stdout.Bytes()).OK || stderr.Len() != 0 {
		t.Fatal("skip logging contaminated JSON output")
	}

	logs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	assertDisabledAutosaveRecords(t, logs)
}

func assertDisabledAutosaveRecords(t *testing.T, logs []byte) {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(logs), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected start and skip without generic success: %s", logs)
	}

	assertRuntimeRecord(t, lines[0], "cli", "save-if-due", "info", "started")

	var record map[string]any
	if err := json.Unmarshal(lines[1], &record); err != nil {
		t.Fatal(err)
	}

	delete(record, "time")

	want := map[string]any{
		"level": "info", "component": "snapshot", "operation": "save-if-due", "status": "skipped",
		"reason": "disabled", "message": "automatic snapshot operation skipped",
	}
	if diff := cmp.Diff(want, record); diff != "" {
		t.Fatalf("incorrect skip record: %s", diff)
	}
}

func TestAgentTrackerQuietPreservesFailureAndSuppressesInformationalRecords(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			testQuietTrackerOutcome(t, failed)
		})
	}
}

func TestAgentNavigationCommandsPersistTheirSuccessOutcome(t *testing.T) {
	for _, operation := range []string{"next", "back", "latest", "focus", "waiting", "failed", "unread", "toggle"} {
		t.Run(operation, func(t *testing.T) {
			cfg, path := runtimeConfig(t, "info")
			root := newRootCommand(t.Context())

			cmd, _, err := root.Find([]string{"agent", operation})
			if err != nil {
				t.Fatal(err)
			}

			cmd.RunE = func(*cobra.Command, []string) error { return nil }

			args := []string{"agent", operation, testConfigFlag, cfg}
			if operation == "focus" {
				args = append(args, "%1")
			}

			root.SetArgs(args)

			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			runtime := commandRuntime{cmd: root}
			root.PersistentPreRunE = runtime.prepare

			_, runErr := root.ExecuteContextC(t.Context())
			if runErr != nil {
				t.Fatal(runErr)
			}

			if err := runtime.finish(t.Context(), runErr); err != nil {
				t.Fatal(err)
			}

			assertRuntimeLog(t, path, "cli", operation, "info", "ok", cfg)

			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("navigation logs contaminated command streams")
			}
		})
	}
}

func testQuietTrackerOutcome(t *testing.T, failed bool) {
	t.Helper()
	cfg, path := runtimeConfig(t, "info")
	root := newRootCommand(t.Context())

	cmd, _, err := root.Find([]string{"daemon", "agent-window-status"})
	if err != nil {
		t.Fatal(err)
	}

	var outcome error
	if failed {
		outcome = errSensitiveRaw
	}

	cmd.RunE = func(*cobra.Command, []string) error { return outcome }

	root.SetArgs([]string{"daemon", "agent-window-status", testConfigFlag, cfg, "--quiet"})

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	runtime := commandRuntime{cmd: root}
	root.PersistentPreRunE = runtime.prepare

	_, runErr := root.ExecuteContextC(t.Context())
	if !errors.Is(runErr, outcome) {
		t.Fatalf("lost tracker outcome: %v", runErr)
	}

	if err := runtime.finish(t.Context(), runErr); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !failed && len(data) != 0 {
		t.Fatalf("quiet informational logs: %s", data)
	}

	if failed {
		if bytes.Count(data, []byte("\n")) != 1 {
			t.Fatalf("expected one failure: %s", data)
		}

		assertRuntimeRecord(t, bytes.TrimSpace(data), "cli", "agent_status_run", "error", "failed")
	}
}
