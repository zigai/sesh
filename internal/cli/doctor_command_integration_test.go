//go:build integration

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/doctor"
)

func TestDoctorApplicationWarningOnlyJSONSucceeds(t *testing.T) {
	base := t.TempDir()
	cfg, logPath := runtimeConfig(t, "info")

	// A repository directory without a repository warns on every platform.
	repository := filepath.Join(base, "empty-repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	err := ExecuteApplication(t.Context(), []string{
		"doctor",
		testSocketFlag, tmuxtest.NewServer(t).Endpoint().SocketPath,
		"--history", filepath.Join(base, "missing-history.json"),
		testRepositoryFlag, repository,
		testConfigFlag, cfg,
		testJSONFlag,
	}, nil, &stdout, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("warning-only doctor failed: %v / %q", err, stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())
	report := decodeDoctorReport(t, envelope.Result)

	if !envelope.OK || envelope.Error != nil || report.Summary.Fail != 0 ||
		report.Summary.Warn == 0 {
		t.Fatalf("invalid warning-only receipt: %s", stdout.String())
	}

	if _, statErr := os.Stat(logPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("doctor opened its log file: %v", statErr)
	}
}

func TestDoctorCommandPlainReportListsGroupsChecksAndVerdict(t *testing.T) {
	t.Parallel()

	stdout, _, _ := executeCommand(
		t,
		"doctor",
		testNoConfigFlag,
		testSocketFlag,
		filepath.Join(t.TempDir(), "absent.sock"),
	)

	for _, want := range []string{
		"\n  socket      ",
		"\nTmux\n",
		"\n  fail  server",
		"\nSnapshots\n",
		" attention.\n",
		" ok, ",
		" fail, ",
	} {
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
		t.Fatalf(
			"expected doctor to exit %d on a missing server, got %v",
			exitCodeGeneral,
			exec.err,
		)
	}

	server := findDoctorCheck(
		decodeDoctorReport(t, exec.envelope.Result).Checks,
		doctor.CheckTmuxServer,
	)
	if server.Status != doctor.StatusFail || server.Reason != doctor.ReasonNotRunning {
		t.Fatalf("expected a not-running server failure: %+v", server)
	}
}
