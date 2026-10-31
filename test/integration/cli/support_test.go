//go:build integration

package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/config"
)

const (
	snapshotCmd               = "snapshot"
	sessionCmd                = "session"
	testConfigPathFlag        = "--path"
	contractStatusField       = "status"
	contractReasonField       = "reason"
	testRepositoryFlag        = "--repository"
	testSocketFlag            = "--socket"
	testLeaseRootFlag         = "--lease-root"
	testSessionFlag           = "--session"
	testProcessEnrichmentFlag = "--process-enrichment"
	testProcessEnvFlag        = "--process-env"
	testGenerationFlag        = "--generation"
	testJSONFlag              = "--json"
	testConfigFlag            = "--config"
	testNoConfigFlag          = "--no-config"
	pickerClientEnvironment   = "SESH_TMUX_CLIENT"
	terminalAvailabilityProbe = "SESH_TEST_TERMINAL_AVAILABILITY"
)

func ExitCode(err error) int {
	return cli.ExitCode(err)
}

var camelCaseKeyPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

type jsonErrorPayload struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode"`
}

type testJSONEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	Command       string            `json:"command"`
	OK            bool              `json:"ok"`
	Result        json.RawMessage   `json:"result"`
	Error         *jsonErrorPayload `json:"error"`
}

type commandExecution struct {
	stdout   string
	stderr   string
	err      error
	envelope testJSONEnvelope
}

func newCaptureCLIServer(t *testing.T) *gotmux.Server {
	t.Helper()
	server := tmuxtest.NewServer(t)

	windows, err := server.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, window := range windows {
		options := window.Handle().Options()
		if err := options.SetAutomaticRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}

		if err := options.SetAllowRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}
	}

	return server
}

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), args, nil, &stdout, &stderr)

	return stdout.String(), stderr.String(), err
}

func executeJSON(t *testing.T, args ...string) commandExecution {
	t.Helper()

	rawOut, rawErr, err := executeCommand(t, args...)

	target := rawOut
	if target == "" {
		target = rawErr
	}

	var env testJSONEnvelope

	if target != "" {
		decoder := json.NewDecoder(strings.NewReader(target))
		if decErr := decoder.Decode(&env); decErr != nil {
			t.Fatalf("failed to decode JSON envelope from %q (err: %v)\nstdout: %q\nstderr: %q", target, decErr, rawOut, rawErr)
		}

		var extra any
		if extraErr := decoder.Decode(&extra); !errors.Is(extraErr, io.EOF) {
			t.Fatalf("JSON output contains more than one document: %q", target)
		}
	}

	return commandExecution{
		stdout:   rawOut,
		stderr:   rawErr,
		err:      err,
		envelope: env,
	}
}

func decodeJSONPayload[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var val T
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatalf("failed to unmarshal JSON payload: %v\nraw: %s", err, string(raw))
	}

	return val
}

func assertAllKeysCamelCase(t *testing.T, val any, path string) {
	t.Helper()

	switch v := val.(type) {
	case map[string]any:
		for k, child := range v {
			if !camelCaseKeyPattern.MatchString(k) {
				t.Fatalf("key %q at path %q is not lowerCamelCase", k, path)
			}

			assertAllKeysCamelCase(t, child, path+"."+k)
		}
	case []any:
		for i, child := range v {
			assertAllKeysCamelCase(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
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
		"schemaVersion": float64(2), "command": command, "ok": true, "error": nil,
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

func contractJSONItem(t *testing.T, values []any, key string, want any) map[string]any {
	t.Helper()

	for _, value := range values {
		record, ok := value.(map[string]any)
		if !ok {
			continue
		}

		if record[key] == want {
			return record
		}
	}

	t.Fatalf("no record found with %s=%v in %v", key, want, values)

	return nil
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

func singleRuntimeEnvelope(t *testing.T, data []byte) testJSONEnvelope {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(data))

	var env testJSONEnvelope
	if err := decoder.Decode(&env); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra output %q: %v", data, err)
	}

	return env
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

func assertRuntimeLog(t *testing.T, path, component, operation, level, status string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
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
			"pid", "version", "socket", "action", "error", "error_domain", "error_step", "error_context", "generation", "restore_id",
			"duration_ms", "failed_step", "store_step", "step_reason", "mismatches", "mismatch_reasons", "floating_panes_omitted",
			"restart_panes", "resume_panes", "carried_resume_panes", "unverified_panes", "nonrestartable_panes",
			"resumed_panes", "restarted_panes", "shell_panes", "unrecovered_panes", "directory_fallback_panes":
		case "verified", "skipped", "terminal_text_omitted":
			assertOperationLogField(t, record, key)
		default:
			t.Fatalf("unexpected log field: %s", key)
		}
	}
}

func assertOperationLogField(t *testing.T, record map[string]any, key string) {
	t.Helper()

	operation := "restore"
	if key == "terminal_text_omitted" {
		operation = "save"
	}

	_, isBool := record[key].(bool)
	_, isCount := record[key].(float64)

	typed := isCount
	if key == "verified" {
		typed = isBool
	}

	if !typed || record["operation"] != operation || record["component"] != "snapshot" {
		t.Fatalf("invalid %s field: %#v", key, record)
	}
}

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remoteConnectionsTOML(connections ...config.RemoteConnectionConfig) string {
	var body strings.Builder

	for _, connection := range connections {
		fmt.Fprintf(&body, "\n[[remote.connections]]\nid = %q\nlabel = %q\nhost = %q\nsesh_path = %q\ntmux_socket = %q\n",
			connection.ID, connection.Label, connection.Host, connection.SeshPath, connection.TmuxSocket)
	}

	return body.String()
}
