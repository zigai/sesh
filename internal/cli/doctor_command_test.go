package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/doctor"

	cmddoctor "github.com/zigai/sesh/internal/cli/cmd/doctor"
)

type doctorResultJSON = cmddoctor.ResultJSON

func newDoctorCommand() *cobra.Command {
	return cmddoctor.NewCommand()
}

type failedDoctorFixture struct {
	stdout  []byte
	stderr  []byte
	logs    []byte
	private []string
	err     error
}

type doctorInvocation struct {
	args    []string
	logPath string
	private []string
}

func TestDoctorCommandHelp(t *testing.T) {
	t.Parallel()

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "doctor", "--help")
	if err != nil {
		t.Fatalf("doctor --help failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Inspect tmux environment") {
		t.Fatalf("stdout missing doctor description: %s", stdout)
	}

	if !strings.Contains(stdout, "--socket") || !strings.Contains(stdout, "--repository") {
		t.Fatalf("stdout missing expected flags: %s", stdout)
	}
}

func TestDoctorCommandJSONOutput(t *testing.T) {
	t.Parallel()

	exec := executeJSON(t, "doctor", testNoConfigFlag, testJSONFlag)

	if exec.envelope.Command != "doctor" {
		t.Fatalf("envelope.Command = %q, want doctor", exec.envelope.Command)
	}

	if !strings.Contains(exec.stdout, `"checks":`) {
		t.Fatalf("stdout missing checks array: %s", exec.stdout)
	}
}

func TestDoctorCommandTableOutput(t *testing.T) {
	t.Parallel()

	root := newRootCommand(t.Context())
	stdout, _, _ := executeCommand(root, "doctor", testNoConfigFlag)

	if !strings.Contains(stdout, "Check") || !strings.Contains(stdout, "Status") {
		t.Fatalf("stdout missing table headers: %s", stdout)
	}

	if !strings.Contains(stdout, "tmux") || !strings.Contains(stdout, "snapshot-repository") {
		t.Fatalf("stdout missing check names: %s", stdout)
	}
}

func TestDoctorCommandNonexistentSocketFails(t *testing.T) {
	t.Parallel()

	nonexistent := "/tmp/definitely-does-not-exist-" + t.Name() + ".sock"
	exec := executeJSON(t, "doctor", "--socket", nonexistent, testNoConfigFlag, testJSONFlag)

	if exec.err == nil {
		t.Fatal("expected doctor error on nonexistent socket, got nil")
	}

	if !strings.Contains(exec.stdout, `"status":"error"`) && !strings.Contains(exec.stdout, `"unavailable"`) {
		t.Fatalf("expected error/unavailable status in stdout: %s", exec.stdout)
	}
}

func TestDoctorApplicationFailedChecksRetainJSONReportAndOwnOutcome(t *testing.T) {
	fixture := runFailedDoctorApplication(t, true)
	if !errors.Is(fixture.err, errDoctorChecksFailed) || ExitCode(fixture.err) != exitCodeGeneral {
		t.Fatalf("unexpected outcome: %v", fixture.err)
	}

	if len(fixture.stderr) != 0 {
		t.Fatalf("failed doctor wrote stderr: %q", fixture.stderr)
	}

	assertFailedDoctorEnvelope(t, fixture.stdout)
	assertDoctorOutputPrivacy(t, fixture)
	assertDoctorRuntimeLog(t, fixture.logs)
}

func TestDoctorApplicationFailedChecksRetainPlainReport(t *testing.T) {
	fixture := runFailedDoctorApplication(t, false)
	if !errors.Is(fixture.err, errDoctorChecksFailed) || ExitCode(fixture.err) != exitCodeGeneral {
		t.Fatalf("unexpected outcome: %v", fixture.err)
	}

	stdout := string(fixture.stdout)
	if !strings.Contains(stdout, "Check") ||
		!strings.Contains(stdout, doctor.CheckHistoryState) ||
		!strings.Contains(stdout, doctor.ReasonCorrupt) {
		t.Fatalf("failed doctor table was not retained: %s", stdout)
	}

	if string(fixture.stderr) != "doctor checks failed\n" {
		t.Fatalf("unexpected doctor diagnostic: %q", fixture.stderr)
	}

	assertDoctorOutputPrivacy(t, fixture)
	assertDoctorRuntimeLog(t, fixture.logs)
}

func TestDoctorApplicationWarningOnlyJSONOwnsDegradedOutcome(t *testing.T) {
	base := t.TempDir()
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{
		"doctor",
		"--history", filepath.Join(base, "missing-history.json"),
		testRepositoryFlag, filepath.Join(base, "missing-repository"),
		testConfigFlag, cfg,
		testJSONFlag,
	}, nil, &stdout, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("warning-only doctor failed: %v / %q", err, stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())

	var result doctorResultJSON
	if decodeErr := json.Unmarshal(envelope.Result, &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}

	if !envelope.OK || envelope.Error != nil || result.HasErrors || !result.HasWarnings {
		t.Fatalf("invalid warning-only receipt: %s", stdout.String())
	}

	logs, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}

	assertDoctorRuntimeOutcome(t, logs, "warn", "degraded", "checks_degraded")
}

func TestDoctorOutcomeMarkerPreservesIndependentFailures(t *testing.T) {
	t.Run("output", func(t *testing.T) {
		invocation := newFailedDoctorInvocation(t, true)

		var stderr bytes.Buffer

		err := executeApplication(t.Context(), invocation.args, nil, failingRuntimeWriter{}, &stderr)
		if !errors.Is(err, errDoctorChecksFailed) || !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("output failure lost a cause: %v", err)
		}

		if stderr.Len() != 0 {
			t.Fatalf("output failure leaked a second diagnostic: %q", stderr.String())
		}
	})

	t.Run("finalization", testDoctorFinalizationFailure)
}

func testDoctorFinalizationFailure(t *testing.T) {
	t.Helper()

	cmd := newDoctorCommand()
	if err := cmd.Flags().Set(jsonFlagName, "true"); err != nil {
		t.Fatal(err)
	}

	var output, diagnostics commandBuffer
	cmd.SetOut(&output)

	doctorErr := cmddoctor.NewChecksError(NewExitError(exitCodeGeneral, errDoctorChecksFailed))
	runErr := writeJSONResultError(cmd, doctorResultJSON{HasErrors: true}, doctorErr)
	closeErr := io.ErrClosedPipe

	var stdout, stderr bytes.Buffer

	err := publishCommandOutput(cmd, &output, &diagnostics, &stdout, &stderr, runErr, closeErr)
	if !errors.Is(err, errDoctorChecksFailed) || !errors.Is(err, closeErr) {
		t.Fatalf("finalization failure lost a cause: %v", err)
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())
	if envelope.OK || envelope.Error == nil || len(stderr.Bytes()) != 0 {
		t.Fatalf("invalid finalization receipt: %s / %q", stdout.String(), stderr.String())
	}
}

func runFailedDoctorApplication(t *testing.T, jsonOutput bool) failedDoctorFixture {
	t.Helper()

	invocation := newFailedDoctorInvocation(t, jsonOutput)

	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), invocation.args, nil, &stdout, &stderr)

	logs, readErr := os.ReadFile(invocation.logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}

	return failedDoctorFixture{
		stdout:  bytes.Clone(stdout.Bytes()),
		stderr:  bytes.Clone(stderr.Bytes()),
		logs:    logs,
		private: invocation.private,
		err:     err,
	}
}

func newFailedDoctorInvocation(t *testing.T, jsonOutput bool) doctorInvocation {
	t.Helper()

	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg, logPath := runtimeConfig(t, "info")
	historyPath := filepath.Join(base, "private-history-canary.json")
	repositoryPath := filepath.Join(base, "private-repository-canary")
	privateContent := "private-history-content-canary"

	if err := os.WriteFile(historyPath, []byte(privateContent), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"doctor",
		"--history", historyPath,
		testRepositoryFlag, repositoryPath,
		testConfigFlag, cfg,
	}
	if jsonOutput {
		args = append(args, testJSONFlag)
	}

	return doctorInvocation{
		args:    args,
		logPath: logPath,
		private: []string{cfg, historyPath, repositoryPath, privateContent},
	}
}

func assertFailedDoctorEnvelope(t *testing.T, output []byte) {
	t.Helper()

	envelope := singleRuntimeEnvelope(t, output)
	assertFailedDoctorErrorContract(t, envelope, output)

	var result doctorResultJSON
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("decode doctor result: %v", err)
	}

	if !result.HasErrors || !result.HasWarnings || len(result.Checks) != 8 {
		t.Fatalf("detailed result was not retained: %+v", result)
	}

	if !hasCorruptHistoryCheck(result.Checks) {
		t.Fatalf("missing corrupt history check: %+v", result.Checks)
	}
}

func assertFailedDoctorErrorContract(t *testing.T, envelope testJSONEnvelope, output []byte) {
	t.Helper()

	if envelope.Command != "doctor" || envelope.OK || envelope.Error == nil {
		t.Fatalf("invalid failed doctor envelope: %s", output)
	}

	if envelope.Error.Code != "doctorChecksFailed" ||
		envelope.Error.Message != "doctor checks failed" ||
		envelope.Error.ExitCode != exitCodeGeneral {
		t.Fatalf("unstable doctor error contract: %+v", envelope.Error)
	}
}

func hasCorruptHistoryCheck(checks []doctor.Result) bool {
	for _, check := range checks {
		if check.Check == doctor.CheckHistoryState &&
			check.Status == doctor.StatusError && check.Reason == doctor.ReasonCorrupt {
			return true
		}
	}

	return false
}

func assertDoctorOutputPrivacy(t *testing.T, fixture failedDoctorFixture) {
	t.Helper()

	for _, private := range fixture.private {
		if bytes.Contains(fixture.stdout, []byte(private)) ||
			bytes.Contains(fixture.stderr, []byte(private)) ||
			bytes.Contains(fixture.logs, []byte(private)) {
			t.Fatalf("private value %q escaped doctor boundaries", private)
		}
	}
}

func assertDoctorRuntimeLog(t *testing.T, data []byte) {
	t.Helper()

	assertDoctorRuntimeOutcome(t, data, "error", "failed", "checks_failed")
}

func assertDoctorRuntimeOutcome(t *testing.T, data []byte, level, status, reason string) {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("expected CLI start, doctor start, and one doctor outcome: %s", data)
	}

	wants := []struct {
		component string
		operation string
		level     string
		status    string
	}{
		{component: "cli", operation: "doctor", level: "info", status: "started"},
		{component: "doctor", operation: "run", level: "info", status: "started"},
		{component: "doctor", operation: "run", level: level, status: status},
	}

	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode log record %d: %v", i, err)
		}

		want := wants[i]
		if record["component"] != want.component || record["operation"] != want.operation ||
			record["level"] != want.level || record["status"] != want.status {
			t.Fatalf("unexpected log record %d: %s", i, line)
		}

		if i == len(lines)-1 {
			assertDoctorTerminalRecord(t, record, line, status, reason)
		}
	}
}

func assertDoctorTerminalRecord(
	t *testing.T,
	record map[string]any,
	line []byte,
	status string,
	reason string,
) {
	t.Helper()

	if record["reason"] != reason {
		t.Fatalf("terminal doctor record has the wrong reason: %s", line)
	}

	if status == "failed" && record["error_count"] == float64(0) {
		t.Fatalf("failed doctor record lacks the error count: %s", line)
	}

	if status == "degraded" && record["warning_count"] == float64(0) {
		t.Fatalf("degraded doctor record lacks the warning count: %s", line)
	}
}
