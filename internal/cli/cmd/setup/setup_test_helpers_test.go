package setup_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/setup"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

const (
	testSocketFlag   = "--socket"
	testJSONFlag     = "--json"
	testConfigFlag   = "--config"
	testNoConfigFlag = "--no-config"
)

// setupServiceJSON and setupServicesResultJSON decode the setup services wire contract.
type setupServiceJSON struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Purpose  string   `json:"purpose"`
	Path     string   `json:"path"`
	State    string   `json:"state"`
	Changed  bool     `json:"changed"`
	Requires []string `json:"requires,omitempty"`
	Content  string   `json:"content,omitempty"`
}

type setupServicesResultJSON struct {
	Platform string                      `json:"platform"`
	Applied  bool                        `json:"applied"`
	Written  int                         `json:"written"`
	Skipped  []string                    `json:"skipped,omitempty"`
	Blocked  []string                    `json:"blocked,omitempty"`
	Services []setupServiceJSON          `json:"services"`
	Manager  *setup.ServiceRefreshResult `json:"manager,omitempty"`
}

type commandExecution = clitest.Execution

func executeJSON(t *testing.T, args ...string) commandExecution {
	t.Helper()

	return clitest.ExecuteJSON(t, args...)
}

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	exec := clitest.Execute(t, args...)

	return exec.Stdout, exec.Stderr, exec.Err
}

func isolatedSetupHome(t *testing.T) {
	t.Helper()

	t.Setenv("TMUX", "")

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
}

func runtimeConfig(t *testing.T, level string) (string, string) {
	t.Helper()
	base := t.TempDir()
	path, logPath := filepath.Join(base, "config.toml"), filepath.Join(base, "private-log.jsonl")

	data := fmt.Sprintf("[settings]\nlog_level = %q\n[paths]\nlog_file = %q\n", level, logPath)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path, logPath
}

func assertRuntimeLog(t *testing.T, path, component, operation, level, status string, private ...string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range private {
		if bytes.Contains(data, []byte(value)) {
			t.Fatal("private value in log")
		}
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected start and one outcome: %s", data)
	}

	for i, line := range lines {
		wantLevel, wantStatus := "info", "started"
		if i == 1 {
			wantLevel, wantStatus = level, status
		}

		assertRuntimeRecord(t, line, component, operation, wantLevel, wantStatus)
	}
}

func assertRuntimeRecord(t *testing.T, line []byte, component, operation, level, status string) {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatal(err)
	}

	if record["component"] != component || record["operation"] != operation {
		t.Fatalf("wrong boundary: %s", line)
	}

	if record["level"] != level || record["status"] != status {
		t.Fatalf("wrong event: %s", line)
	}

	if status != "started" && status != "ok" && record["reason"] == nil && record["phase"] == nil && record["code"] == nil {
		t.Fatalf("missing fixed reason: %s", line)
	}

	assertRuntimeLogFields(t, record)
}

func assertRuntimeLogFields(t *testing.T, record map[string]any) {
	t.Helper()

	for key := range record {
		switch key {
		case "time", "level", "component", "operation", "status", "message", "reason", "phase", "code", "sessions", "windows", "panes", "error_code",
			"duration_ms", "failed_step", "store_step", "step_reason", "mismatches", "mismatch_reasons", "floating_panes_omitted",
			"restart_panes", "resume_panes", "unverified_panes", "nonrestartable_panes",
			"resumed_panes", "restarted_panes", "shell_panes", "unrecovered_panes", "directory_fallback_panes":
		case "verified":
			if _, ok := record[key].(bool); !ok || record["operation"] != "restore" || record["component"] != "snapshot" {
				t.Fatalf("invalid verification field: %#v", record)
			}
		case "terminal_text_omitted":
			if _, ok := record[key].(float64); !ok || record["operation"] != "save" || record["component"] != "snapshot" {
				t.Fatalf("invalid terminal text count: %#v", record)
			}
		default:
			t.Fatalf("unexpected log field: %s", key)
		}
	}
}
