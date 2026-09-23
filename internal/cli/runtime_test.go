package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestApplicationInformationalCommandsSkipConfiguration(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"snapshot", "--help"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			args = append(args, testConfigFlag, filepath.Join(t.TempDir(), "missing.toml"))
			if err := executeApplication(t.Context(), args, nil, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}

			if stdout.Len() == 0 || stderr.Len() != 0 {
				t.Fatalf("unexpected output: %q / %q", stdout.String(), stderr.String())
			}
		})
	}
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

func TestApplicationReadBoundaryLogging(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(strconv.FormatBool(missing), func(t *testing.T) { testReadBoundaryLogging(t, missing) })
	}
}

func testReadBoundaryLogging(t *testing.T, missing bool) {
	t.Helper()
	cfg, logPath := runtimeConfig(t, "info")

	repoPath := filepath.Join(t.TempDir(), "private-repository-canary")
	if !missing {
		repo, err := store.Open(t.Context(), repoPath)
		if err != nil {
			t.Fatal(err)
		}

		if err := repo.Close(); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{"snapshot", "history", testRepositoryFlag, repoPath, testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if (err != nil) != missing {
		t.Fatalf("result: %v", err)
	}

	var env testJSONEnvelope

	if missing {
		if stdout.Len() != 0 {
			t.Fatal("failure wrote stdout")
		}

		env = singleRuntimeEnvelope(t, stderr.Bytes())
	} else {
		if stderr.Len() != 0 {
			t.Fatal("success wrote stderr")
		}

		env = singleRuntimeEnvelope(t, stdout.Bytes())
	}

	if env.OK == missing {
		t.Fatalf("wrong result: %+v", env)
	}

	level, status := "info", "ok"
	if missing {
		level, status = "error", "failed"
	}

	assertRuntimeLog(t, logPath, "cli", "history", level, status, repoPath, cfg)
}

func TestApplicationDisabledLogging(t *testing.T) {
	cfg, logPath := runtimeConfig(t, "disabled")

	var stdout, stderr bytes.Buffer

	_ = executeApplication(t.Context(), []string{"snapshot", "history", testRepositoryFlag, filepath.Join(t.TempDir(), "missing"), testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled log exists: %v", err)
	}
}

func TestApplicationNoConfigStillAppliesExplicitEnvironment(t *testing.T) {
	base := t.TempDir()
	logPath := filepath.Join(base, "env-log.jsonl")
	t.Setenv("SESH_PATHS_LOG_FILE", logPath)
	t.Setenv("SESH_SETTINGS_LOG_LEVEL", "error")

	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{"snapshot", "history", testRepositoryFlag, filepath.Join(base, "missing"), testConfigFlag, filepath.Join(base, "missing.toml"), testNoConfigFlag, testJSONFlag}, nil, &stdout, &stderr)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected outcome: %v", err)
	}

	singleRuntimeEnvelope(t, stderr.Bytes())

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("level filtering failed: %s", data)
	}

	assertRuntimeRecord(t, lines[0], "cli", "history", "error", "failed")
}

func TestApplicationUsageFailureDoesNotOpenLog(t *testing.T) {
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{actionCmd, "snapshot-save", testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if ExitCode(err) != exitCodeUsage {
		t.Fatalf("unexpected outcome: %v", err)
	}

	singleRuntimeEnvelope(t, stderr.Bytes())

	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("usage touched log: %v", err)
	}
}

func TestApplicationUnknownFlagDoesNotEchoPrivateText(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{"snapshot", "history", "--secret-token-canary", testJSONFlag}, nil, &stdout, &stderr)
	if ExitCode(err) != exitCodeUsage {
		t.Fatalf("unexpected outcome: %v", err)
	}

	env := singleRuntimeEnvelope(t, stderr.Bytes())
	if bytes.Contains(stderr.Bytes(), []byte("secret-token-canary")) || env.Error == nil || !strings.Contains(env.Error.Message, "unknown flag") || !strings.Contains(env.Error.Message, "snapshot history --help") {
		t.Fatalf("unsafe diagnostic: %s", stderr.Bytes())
	}
}

func TestApplicationConfigurationUsesProvidedInput(t *testing.T) {
	base := t.TempDir()
	logPath := filepath.Join(base, "input-log.jsonl")
	input := strings.NewReader(fmt.Sprintf("[paths]\nlog_file = %q\n", logPath))

	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{"snapshot", "history", testRepositoryFlag, filepath.Join(base, "missing"), testConfigFlag, "-", testJSONFlag}, input, &stdout, &stderr)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected outcome: %v", err)
	}

	singleRuntimeEnvelope(t, stderr.Bytes())
	assertRuntimeLog(t, logPath, "cli", "history", "error", "failed", logPath)
}

func TestApplicationConfigDiagnosticsRetainLocationWithoutPrivateValues(t *testing.T) {
	var stdout, stderr bytes.Buffer

	input := strings.NewReader("[settings]\nsecret_private_key = 1\n")

	err := executeApplication(t.Context(), []string{"snapshot", "history", testRepositoryFlag, filepath.Join(t.TempDir(), "missing"), testConfigFlag, "-", testJSONFlag}, input, &stdout, &stderr)
	if err == nil {
		t.Fatal("invalid config accepted")
	}

	env := singleRuntimeEnvelope(t, stderr.Bytes())
	if env.Error == nil || env.Error.Code != "configurationFailed" || !strings.Contains(env.Error.Message, "line 2") {
		t.Fatalf("wrong configuration diagnostic: %s", stderr.Bytes())
	}

	if bytes.Contains(stderr.Bytes(), []byte("secret_private_key")) {
		t.Fatal("private key echoed")
	}
}

func TestApplicationSaveOwnsOneOutcome(t *testing.T) {
	server := newCaptureCLIServer(t)

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, failed := range []bool{false, true} {
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			cfg, logPath := runtimeConfig(t, "info")

			socket := server.Endpoint().SocketPath
			if failed {
				socket = filepath.Join(t.TempDir(), "missing.sock")
			}

			repoPath := filepath.Join(t.TempDir(), "private-repository-canary")
			args := []string{actionCmd, "snapshot-save", testRepositoryFlag, repoPath, testSocketFlag, socket, testLeaseRootFlag, filepath.Join(t.TempDir(), "leases"), testSessionFlag, string(sessions[0].ID), testConfigFlag, cfg, testJSONFlag}

			var stdout, stderr bytes.Buffer

			err := executeApplication(t.Context(), args, nil, &stdout, &stderr)

			payload := stdout.Bytes()
			if len(payload) == 0 {
				payload = stderr.Bytes()
			} else if stderr.Len() != 0 {
				t.Fatal("two output streams")
			}

			env := singleRuntimeEnvelope(t, payload)
			level, status := assertApplicationSaveOutcome(t, failed, err, env)

			assertRuntimeLog(t, logPath, "snapshot", "save", level, status, repoPath, cfg, socket)
		})
	}
}

func assertApplicationSaveOutcome(t *testing.T, failed bool, err error, env testJSONEnvelope) (string, string) {
	t.Helper()

	if failed {
		if err == nil || env.OK || env.Error == nil {
			t.Fatalf("invalid failed save outcome: err=%v envelope=%+v", err, env)
		}

		return "error", "failed"
	}

	if err != nil || !env.OK || env.Error != nil {
		t.Fatalf("invalid practical save outcome: err=%v envelope=%+v", err, env)
	}

	return "warn", "best_effort"
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
		case "time", "level", "component", "operation", "status", "message", "reason", "phase", "code", "sessions", "windows", "panes", "error_code":
		case "verified":
			if _, ok := record[key].(bool); !ok || record["operation"] != "restore" || record["component"] != "snapshot" {
				t.Fatalf("invalid verification field: %#v", record)
			}
		default:
			t.Fatalf("unexpected log field: %s", key)
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

func TestFinalizationFailurePreservesResultAndUncertainty(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(strconv.FormatBool(uncertain), func(t *testing.T) { testFinalizationFailure(t, uncertain) })
	}
}

func testFinalizationFailure(t *testing.T, uncertain bool) {
	t.Helper()

	cmd := cmdsnapshot.NewActionSaveCommand()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}

	var output, diagnostics commandBuffer

	_, err := output.Write([]byte(`{"schemaVersion":1,"ok":true,"result":{"generation":"9007199254740993","sequence":9007199254740993},"error":null}`))
	if err != nil {
		t.Fatal(err)
	}

	closeErr := io.ErrClosedPipe

	var runErr error
	if uncertain {
		runErr = clibase.NewRenderedCommandError(store.ErrUncertain)
	}

	var stdout, stderr bytes.Buffer

	err = publishCommandOutput(cmd, &output, &diagnostics, &stdout, &stderr, runErr, closeErr)
	if !errors.Is(err, closeErr) || (uncertain && !errors.Is(err, store.ErrUncertain)) {
		t.Fatalf("lost cause: %v", err)
	}

	assertFinalizationOutput(t, stdout.Bytes(), stderr.Bytes(), uncertain)
}

func assertFinalizationOutput(t *testing.T, stdout, stderr []byte, uncertain bool) {
	t.Helper()

	env := singleRuntimeEnvelope(t, stdout)
	if env.OK || env.Error == nil || len(stderr) != 0 {
		t.Fatalf("invalid finalization output: %+v / %q", env, stderr)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(env.Result, &fields); err != nil {
		t.Fatal(err)
	}

	if string(fields["sequence"]) != "9007199254740993" {
		t.Fatalf("result changed: %s", env.Result)
	}

	if uncertain && env.Error.Code != "publicationUncertain" {
		t.Fatalf("wrong uncertainty: %+v", env.Error)
	}
}

func TestCommandBufferBoundsStringWrites(t *testing.T) {
	var output commandBuffer

	_, err := io.WriteString(&output, strings.Repeat("x", maxCommandOutput+1))
	if !errors.Is(err, errCommandOutputLimit) || !output.failed || len(output.Bytes()) != 0 {
		t.Fatalf("limit bypass: %v", err)
	}
}

func TestFinalizationWithoutResultRetainsUncertaintyDiagnostic(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(strconv.FormatBool(jsonMode), func(t *testing.T) {
			cmd := cmdsnapshot.NewActionSaveCommand()
			if err := cmd.Flags().Set("json", strconv.FormatBool(jsonMode)); err != nil {
				t.Fatal(err)
			}

			var (
				output, diagnostics commandBuffer
				stdout, stderr      bytes.Buffer
			)

			err := publishCommandOutput(cmd, &output, &diagnostics, &stdout, &stderr, clibase.NewRenderedCommandError(store.ErrUncertain), io.ErrClosedPipe)
			if !errors.Is(err, store.ErrUncertain) || !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("lost causes: %v", err)
			}

			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "durability uncertain") {
				t.Fatalf("wrong streams: %q / %q", stdout.String(), stderr.String())
			}

			if jsonMode {
				singleRuntimeEnvelope(t, stderr.Bytes())
			}
		})
	}
}

func TestApplicationOutputFailurePreservesCause(t *testing.T) {
	var stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{"--version"}, nil, failingRuntimeWriter{}, &stderr)
	if !errors.Is(err, io.ErrClosedPipe) || stderr.Len() != 0 {
		t.Fatalf("output failure: %v / %q", err, stderr.String())
	}
}

type failingRuntimeWriter struct{}

func TestHumanFinalizationReportsPrimaryAndCleanupFailures(t *testing.T) {
	cmd := cmdsnapshot.NewActionSaveCommand()

	var (
		output, diagnostics commandBuffer
		stdout, stderr      bytes.Buffer
	)

	err := publishCommandOutput(cmd, &output, &diagnostics, &stdout, &stderr, store.ErrRecoveryRequired, io.ErrClosedPipe)
	if !errors.Is(err, store.ErrRecoveryRequired) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost cause: %v", err)
	}

	if stderr.String() != "latest save requires explicit recovery\noperation failed\n" {
		t.Fatalf("missing diagnostic: %q", stderr.String())
	}
}

func (failingRuntimeWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type shortWriteReviewWriter func([]byte) (int, error)

func (w shortWriteReviewWriter) Write(p []byte) (int, error) { return w(p) }

func TestStreamingBufferDetectsShortWrite(t *testing.T) {
	writer := shortWriteReviewWriter(func([]byte) (int, error) { return 0, nil })

	output := commandBuffer{output: writer, streaming: true}
	if _, err := output.Write([]byte("record\n")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write accepted: %v", err)
	}
}

func TestLogStreamDiagnosticsPreserveIndependentFailures(t *testing.T) {
	cmd, _, err := newRootCommand(t.Context()).Find([]string{"logs"})
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	cause := errors.Join(context.Canceled, io.ErrUnexpectedEOF)
	if err := writeStreamDiagnosticError(cmd, cause); err != nil {
		t.Fatal(err)
	}

	if stderr.Len() == 0 || ExitCode(cause) == 0 || ExitCode(cause) == exitCodeCancel {
		t.Fatalf("independent failure hidden: %q, exit %d", stderr.String(), ExitCode(cause))
	}
}

func TestRestoreRuntimeLogOwnership(t *testing.T) {
	t.Parallel()

	engineFailure := clibase.NewRestoreBoundaryError(restore.ErrExecutionUncertain)

	cases := []struct {
		name       string
		err        error
		wantRecord bool
	}{
		{"complete", nil, false},
		{"partial", clibase.NewRenderedCommandError(clibase.ErrPartialRestore), false},
		{"engine_failure", clibase.NewCommandResultError(engineFailure), false},
		{"independent_cleanup", errors.Join(engineFailure, errSensitiveRaw), true},
		{"admission_failure", errSensitiveRaw, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "restore.jsonl")

			sink, err := logging.Open(path)
			if err != nil {
				t.Fatal(err)
			}

			cmd, _, findErr := newRootCommand(t.Context()).Find([]string{actionCmd, "snapshot-restore"})
			if findErr != nil {
				t.Fatal(findErr)
			}

			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			runtime := commandRuntime{cmd: cmd, sink: sink, operation: "restore", output: nil, diagnostics: nil}
			if err := runtime.finish(t.Context(), tc.err); err != nil {
				t.Fatal(err)
			}

			assertRestoreBoundaryRecords(t, path, tc.wantRecord)

			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("persistent logging contaminated command streams")
			}
		})
	}
}

func assertRestoreBoundaryRecords(t *testing.T, path string, wantRecord bool) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !wantRecord {
		if len(data) != 0 {
			t.Fatalf("duplicated engine outcome: %s", data)
		}

		return
	}

	if bytes.Count(data, []byte("\n")) != 1 {
		t.Fatalf("expected one CLI cleanup/admission failure: %s", data)
	}

	assertRuntimeRecord(t, bytes.TrimSpace(data), "cli", "restore", "error", "failed")

	if !bytes.Contains(data, []byte(`"reason"`)) || bytes.Contains(data, []byte("/private/")) {
		t.Fatalf("unsafe failure schema: %s", data)
	}
}

func TestRestoreTextReceiptSurvivesStagedFailure(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	output := commandBuffer{buffer: bytes.Buffer{}, output: &stdout, failed: false, streaming: false}
	diagnostics := commandBuffer{buffer: bytes.Buffer{}, output: &stderr, failed: false, streaming: false}

	receipt := "Operation: 0123456789abcdef0123456789abcdef\n"
	if _, err := output.Write([]byte(receipt)); err != nil {
		t.Fatal(err)
	}

	err := clibase.NewCommandResultError(restore.ErrExecutionUncertain)
	if err := copyStagedBuffers(&output, &diagnostics, &stdout, &stderr, err); err != nil {
		t.Fatal(err)
	}

	if stdout.String() != receipt || stderr.Len() != 0 {
		t.Fatal("restore receipt was discarded")
	}

	cmd, _, findErr := newRootCommand(t.Context()).Find([]string{actionCmd, "snapshot-restore"})
	if findErr != nil {
		t.Fatal(findErr)
	}

	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := clibase.WriteCommandError(cmd, err); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stderr.String(), "restore effect outcome uncertain") {
		t.Fatal("text receipt suppressed its diagnostic")
	}
}

func TestRestoreUncertaintySurvivesLogFinalizationFailure(t *testing.T) {
	t.Parallel()

	cmd, _, findErr := newRootCommand(t.Context()).Find([]string{actionCmd, "snapshot-restore"})
	if findErr != nil {
		t.Fatal(findErr)
	}

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}

	output := commandBuffer{buffer: bytes.Buffer{}, output: &stdout, failed: false, streaming: false}
	if _, err := output.Write([]byte(`{"result":{"operationId":"0123456789abcdef0123456789abcdef"}}`)); err != nil {
		t.Fatal(err)
	}

	err := publishFinalizationFailure(cmd, &output, restore.ErrExecutionUncertain, errSensitiveRaw)
	if !errors.Is(err, restore.ErrExecutionUncertain) || !errors.Is(err, errSensitiveRaw) {
		t.Fatal("finalization lost an independent cause")
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "operationUncertain" || stderr.Len() != 0 {
		t.Fatalf("uncertainty hidden by cleanup failure: %s / %s", stdout.String(), stderr.String())
	}
}
