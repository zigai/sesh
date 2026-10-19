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

	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/doctor"

	cmddoctor "github.com/zigai/sesh/internal/cli/cmd/doctor"
)

type failedDoctorFixture struct {
	stdout  []byte
	stderr  []byte
	logPath string
	private string
	err     error
}

func TestDoctorCommandHelp(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, "doctor", "--help")
	if err != nil {
		t.Fatalf("doctor --help failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Check tmux, configuration, snapshots and services") {
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

	report := decodeDoctorReport(t, exec.envelope.Result)
	if len(report.Checks) == 0 || report.Version == "" || report.Paths.Socket.Path == "" {
		t.Fatalf("report lacks checks or identity: %s", exec.stdout)
	}

	if config := report.Paths.Config; len(config) != 2 || config[0].Status != doctor.ConfigSkipped || config[1].Status != doctor.ConfigSkipped {
		t.Fatalf("--no-config must mark every config file skipped: %+v", config)
	}
}

func TestDoctorCommandPlainReportListsGroupsChecksAndVerdict(t *testing.T) {
	t.Parallel()

	stdout, _, _ := executeCommand(t, "doctor", testNoConfigFlag, testSocketFlag, filepath.Join(t.TempDir(), "absent.sock"))

	for _, want := range []string{"\n  socket      ", "\nTmux\n", "\n  fail  server", "\nSnapshots\n", "need attention.\n", " ok, ", " fail, "} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorCommandNonexistentSocketFails(t *testing.T) {
	t.Parallel()

	nonexistent := filepath.Join(t.TempDir(), "absent.sock")
	exec := executeJSON(t, "doctor", testSocketFlag, nonexistent, testNoConfigFlag, testJSONFlag)

	if exec.err == nil || ExitCode(exec.err) != exitCodeGeneral {
		t.Fatalf("expected doctor to exit %d on a missing server, got %v", exitCodeGeneral, exec.err)
	}

	server := findDoctorCheck(decodeDoctorReport(t, exec.envelope.Result).Checks, doctor.CheckTmuxServer)
	if server.Status != doctor.StatusFail || server.Reason != doctor.ReasonNotRunning {
		t.Fatalf("expected a not-running server failure: %+v", server)
	}
}

func TestDoctorSurvivesABrokenConfigAndReportsIt(t *testing.T) {
	t.Parallel()

	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("garbage = =\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(t, "doctor", testConfigFlag, cfg, testSocketFlag, filepath.Join(t.TempDir(), "absent.sock"), testJSONFlag)

	report := decodeDoctorReport(t, exec.envelope.Result)

	config := findDoctorCheck(report.Checks, doctor.CheckConfigFile)
	if config.Status != doctor.StatusFail || !strings.HasPrefix(config.Message, "Invalid configuration at line 1") {
		t.Fatalf("broken config must fail its own check: %+v", config)
	}

	if len(report.Checks) < 10 || findDoctorCheck(report.Checks, doctor.CheckSnapshotRepository).ID == "" {
		t.Fatalf("the remaining checks must still run: %+v", report.Checks)
	}
}

func TestDoctorApplicationFailedChecksRetainJSONReport(t *testing.T) {
	fixture := runFailedDoctorApplication(t, true)
	if !errors.Is(fixture.err, errDoctorChecksFailed) || ExitCode(fixture.err) != exitCodeGeneral {
		t.Fatalf("unexpected outcome: %v", fixture.err)
	}

	if len(fixture.stderr) != 0 {
		t.Fatalf("failed doctor wrote stderr: %q", fixture.stderr)
	}

	assertFailedDoctorEnvelope(t, fixture.stdout)
	assertDoctorLeftNoTrace(t, fixture)
}

func TestDoctorApplicationFailedChecksRetainPlainReport(t *testing.T) {
	fixture := runFailedDoctorApplication(t, false)
	if !errors.Is(fixture.err, errDoctorChecksFailed) || ExitCode(fixture.err) != exitCodeGeneral {
		t.Fatalf("unexpected outcome: %v", fixture.err)
	}

	stdout := string(fixture.stdout)
	if !strings.Contains(stdout, "\nHistory\n  fail  file") || !strings.Contains(stdout, "need attention.\n") {
		t.Fatalf("failed doctor report was not retained: %s", stdout)
	}

	if len(fixture.stderr) != 0 {
		t.Fatalf("the report already states the failure, yet stderr got %q", fixture.stderr)
	}

	assertDoctorLeftNoTrace(t, fixture)
}

func TestDoctorApplicationWarningOnlyJSONSucceeds(t *testing.T) {
	base := t.TempDir()
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	err := ExecuteApplication(t.Context(), []string{
		"doctor",
		testSocketFlag, tmuxtest.NewServer(t).Endpoint().SocketPath,
		"--history", filepath.Join(base, "missing-history.json"),
		testRepositoryFlag, filepath.Join(base, "missing-repository"),
		testConfigFlag, cfg,
		testJSONFlag,
	}, nil, &stdout, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("warning-only doctor failed: %v / %q", err, stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())
	report := decodeDoctorReport(t, envelope.Result)

	if !envelope.OK || envelope.Error != nil || report.Summary.Fail != 0 || report.Summary.Warn == 0 {
		t.Fatalf("invalid warning-only receipt: %s", stdout.String())
	}

	if _, statErr := os.Stat(logPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("doctor opened its log file: %v", statErr)
	}
}

func TestDoctorOutputFailureKeepsTheChecksFailure(t *testing.T) {
	fixture := newFailedDoctorArgs(t, true)

	var stderr bytes.Buffer

	err := ExecuteApplication(t.Context(), fixture, nil, failingRuntimeWriter{}, &stderr)
	if !errors.Is(err, errDoctorChecksFailed) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure lost a cause: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("output failure leaked a second diagnostic: %q", stderr.String())
	}
}

func runFailedDoctorApplication(t *testing.T, jsonOutput bool) failedDoctorFixture {
	t.Helper()

	cfg, logPath := runtimeConfig(t, "info")
	args := append(newFailedDoctorArgs(t, jsonOutput), testConfigFlag, cfg)

	var stdout, stderr bytes.Buffer

	err := ExecuteApplication(t.Context(), args, nil, &stdout, &stderr)

	return failedDoctorFixture{
		stdout:  bytes.Clone(stdout.Bytes()),
		stderr:  bytes.Clone(stderr.Bytes()),
		logPath: logPath,
		private: privateHistoryContent,
		err:     err,
	}
}

const privateHistoryContent = "private-history-content-canary"

func newFailedDoctorArgs(t *testing.T, jsonOutput bool) []string {
	t.Helper()

	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}

	historyPath := filepath.Join(base, "history.json")
	if err := os.WriteFile(historyPath, []byte(privateHistoryContent), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"doctor",
		"--history", historyPath,
		testRepositoryFlag, filepath.Join(base, "repository"),
		testSocketFlag, filepath.Join(base, "absent.sock"),
	}
	if jsonOutput {
		args = append(args, testJSONFlag)
	}

	return args
}

func assertFailedDoctorEnvelope(t *testing.T, output []byte) {
	t.Helper()

	envelope := singleRuntimeEnvelope(t, output)
	if envelope.Command != "doctor" || envelope.OK || envelope.Error == nil {
		t.Fatalf("invalid failed doctor envelope: %s", output)
	}

	if envelope.Error.Code != "doctorChecksFailed" ||
		envelope.Error.Message != "doctor checks failed" ||
		envelope.Error.ExitCode != exitCodeGeneral {
		t.Fatalf("unstable doctor error contract: %+v", envelope.Error)
	}

	report := decodeDoctorReport(t, envelope.Result)

	history := findDoctorCheck(report.Checks, doctor.CheckHistoryFile)
	if history.Status != doctor.StatusFail || history.Reason != doctor.ReasonCorrupt || history.Fix == nil {
		t.Fatalf("missing corrupt history check: %+v", report.Checks)
	}

	if report.Summary.Fail == 0 {
		t.Fatalf("summary does not count the failures: %+v", report.Summary)
	}
}

func assertDoctorLeftNoTrace(t *testing.T, fixture failedDoctorFixture) {
	t.Helper()

	if bytes.Contains(fixture.stdout, []byte(fixture.private)) || bytes.Contains(fixture.stderr, []byte(fixture.private)) {
		t.Fatalf("private history content escaped the doctor")
	}

	if _, err := os.Stat(fixture.logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor opened its log file: %v", err)
	}
}

func decodeDoctorReport(t *testing.T, data json.RawMessage) cmddoctor.Report {
	t.Helper()

	var report cmddoctor.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode doctor report %s: %v", data, err)
	}

	return report
}

func findDoctorCheck(checks []doctor.Result, id string) doctor.Result {
	for _, check := range checks {
		if check.ID == id {
			return check
		}
	}

	return doctor.Result{}
}
