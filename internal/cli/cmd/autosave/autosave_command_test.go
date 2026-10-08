package autosave_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/testsupport/clitest"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func TestAutosaveCommandHelp(t *testing.T) {
	t.Parallel()

	subcommands := []string{"status"}

	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			stdout, stderr, err := executeCommand(t, "autosave", sub, "--help")
			if err != nil {
				t.Fatalf("autosave %s --help failed: %v\nstderr: %s", sub, err, stderr)
			}

			if !strings.Contains(stdout, sub) {
				t.Fatalf("stdout missing %s in help: %s", sub, stdout)
			}
		})
	}
}

func TestActionAutosaveCommandsHelp(t *testing.T) {
	t.Parallel()

	actions := []string{"autosave-save-if-due", "autosave-restore-on-start", "autosave-final-save"}

	for _, actionName := range actions {
		t.Run(actionName, func(t *testing.T) {
			stdout, stderr, err := executeCommand(t, actionCmd, actionName, "--help")
			if err != nil {
				t.Fatalf("action %s --help failed: %v\nstderr: %s", actionName, err, stderr)
			}

			if !strings.Contains(stdout, actionName) {
				t.Fatalf("stdout missing %s in help: %s", actionName, stdout)
			}
		})
	}
}

func TestAutosaveStatusReportsDueStateWithDefaults(t *testing.T) {
	isolatedAutosaveHome(t)

	exec := executeJSON(t, "autosave", "status", testNoConfigFlag, testJSONFlag)
	if !exec.Envelope.OK {
		t.Fatalf("autosave status failed: %s", exec.Stderr)
	}

	result := clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)

	if result.Status != "due" {
		t.Fatalf("status = %q, want due", result.Status)
	}

	if result.Reason != "never_run" {
		t.Fatalf("reason = %q, want never_run", result.Reason)
	}

	if result.IntervalM != 3 {
		t.Fatalf("intervalMinutes = %d, want 3", result.IntervalM)
	}
}

func TestAutosaveDisabledIsANoOpWithoutSocket(t *testing.T) {
	isolatedAutosaveHome(t)

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[autosave]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, command := range [][]string{{"autosave", "status"}, {actionCmd, "autosave-save-if-due"}} {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			exec := executeJSON(t, append(command, testConfigFlag, cfgPath, testJSONFlag)...)
			if !exec.Envelope.OK {
				t.Fatalf("disabled autosave %v must not fail: %s", command, exec.Stderr)
			}

			result := clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)
			if result.Status != "disabled" || result.Reason != "disabled" {
				t.Fatalf("result = %#v, want disabled/disabled", result)
			}
		})
	}
}

func TestAutosaveSaveIfDueSkipsWhenDefaultTmuxServerIsNotRunning(t *testing.T) {
	isolatedAutosaveHome(t)

	exec := executeJSON(t, actionCmd, "autosave-save-if-due", testNoConfigFlag, testJSONFlag)
	if exec.Err != nil || !exec.Envelope.OK {
		t.Fatalf("save-if-due without a running tmux server failed: %v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)
	if result.Status != "skipped" || result.Reason != "tmux_not_running" || result.Recorded {
		t.Fatalf("result = %#v, want an unrecorded skip because tmux is not running", result)
	}

	if result.Attempt != "" || result.Generation != "" {
		t.Fatalf("result = %#v, want no save attempt without a tmux server", result)
	}
}

func TestAutosaveStatusReportsHistoryUnavailable(t *testing.T) {
	stateHome := isolatedAutosaveHome(t)
	writeCorruptHistoryState(t, stateHome)

	status := executeJSON(t, "autosave", "status", testNoConfigFlag, testJSONFlag)
	if status.Err == nil {
		t.Fatal("autosave status with an unreadable history must report an error")
	}

	if clibase.ExitCode(status.Err) != clibase.ExitCodeGeneral {
		t.Fatalf("status exit code = %d, want %d", clibase.ExitCode(status.Err), clibase.ExitCodeGeneral)
	}

	if status.Envelope.Error == nil {
		t.Fatal("degraded status must carry an error payload")
	}

	degraded := clitest.DecodePayload[autosaveResultJSON](t, status.Envelope.Result)
	if degraded.Status != "degraded" || degraded.Reason != automation.ReasonHistoryUnavailable {
		t.Fatalf("status result = %#v, want degraded/history_unavailable", degraded)
	}

	if degraded.HistoryState != "unavailable" {
		t.Fatalf("historyState = %q, want unavailable", degraded.HistoryState)
	}
}

func TestAutosaveSaveIfDueRefusesWhenHistoryUnavailable(t *testing.T) {
	stateHome := isolatedAutosaveHome(t)
	writeCorruptHistoryState(t, stateHome)

	save := executeJSON(t, actionCmd, "autosave-save-if-due", testNoConfigFlag, testJSONFlag)
	if save.Err == nil {
		t.Fatal("save-if-due with an unreadable history must refuse")
	}

	if clibase.ExitCode(save.Err) != clibase.ExitCodeGeneral {
		t.Fatalf("save exit code = %d, want %d", clibase.ExitCode(save.Err), clibase.ExitCodeGeneral)
	}

	refusal := clitest.DecodePayload[autosaveResultJSON](t, save.Envelope.Result)
	if refusal.Status != "degraded" || refusal.Reason != automation.ReasonHistoryUnavailable {
		t.Fatalf("save result = %#v, want degraded/history_unavailable", refusal)
	}

	if refusal.Generation != "" || refusal.Recorded {
		t.Fatalf("refusal = %#v, want no generation and no recorded completion", refusal)
	}
}

func TestAutosaveStatusReportsDurableState(t *testing.T) {
	isolatedAutosaveHome(t)

	exec := executeJSON(t, "autosave", "status", testNoConfigFlag, testJSONFlag)
	if !exec.Envelope.OK {
		t.Fatalf("autosave status failed: %s", exec.Stderr)
	}

	result := clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)
	if result.HistoryState != "never_run" {
		t.Fatalf("historyState = %q, want never_run", result.HistoryState)
	}

	if result.RepositoryState != "missing" {
		t.Fatalf("repositoryState = %q, want missing", result.RepositoryState)
	}
}

func TestAutosaveStatusReportsBestEffortSaveAsRestorable(t *testing.T) {
	isolatedAutosaveHome(t)

	repoPath := filepath.Join(t.TempDir(), "snapshots")
	snapfixture.CommitOneSession(t, repoPath)

	exec := executeJSON(t, "autosave", "status", testNoConfigFlag, testJSONFlag, "--repository", repoPath)
	if !exec.Envelope.OK {
		t.Fatalf("autosave status failed: %s", exec.Stderr)
	}

	result := clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)
	if result.RepositoryState != "ready" {
		t.Fatalf("repositoryState = %q, want ready for a best-effort save", result.RepositoryState)
	}
}

func TestAutosaveFinalSaveRequiresPositiveDeadline(t *testing.T) {
	isolatedAutosaveHome(t)

	deadline := executeJSON(
		t, actionCmd, "autosave-final-save",
		testNoConfigFlag, testJSONFlag,
		testSocketFlag, filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "sesh.sock"),
		"--deadline", "0s",
	)
	if deadline.Err == nil || clibase.ExitCode(deadline.Err) != clibase.ExitCodeUsage {
		t.Fatalf("final-save with a zero deadline = (%v, %d), want a usage error", deadline.Err, clibase.ExitCode(deadline.Err))
	}
}

func TestAutosaveFinalSaveRejectsInvalidEnvironmentKeys(t *testing.T) {
	isolatedAutosaveHome(t)

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	content := "[snapshot]\nprocess_environment = [\"INVALID=KEY\"]\n"

	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "sesh.sock")

	exec := executeJSON(
		t, actionCmd, "autosave-final-save",
		testConfigFlag, cfgPath,
		testSocketFlag, socket,
		testJSONFlag,
	)

	if exec.Envelope.OK {
		t.Fatal("expected final-save to fail for invalid environment key")
	}

	if exec.Envelope.Error == nil || exec.Envelope.Error.Code != "configurationFailed" {
		t.Fatalf("final-save error = %+v, want configurationFailed", exec.Envelope.Error)
	}
}

func TestAutosaveSaveIfDueRejectsInvalidEnvironmentKeys(t *testing.T) {
	isolatedAutosaveHome(t)

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	content := "[snapshot]\nprocess_environment = [\"INVALID=KEY\"]\n"

	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "sesh.sock")

	exec := executeJSON(
		t, actionCmd, "autosave-save-if-due",
		testConfigFlag, cfgPath,
		testSocketFlag, socket,
		testJSONFlag,
	)

	if exec.Err == nil {
		t.Fatal("expected save-if-due to fail for invalid environment key")
	}
}

func TestAutosaveStatusTextSummarizesScheduleSnapshotsAndShutdownSaves(t *testing.T) {
	isolatedAutosaveHome(t)

	stdout, stderr, err := executeCommand(t, "autosave", "status", testNoConfigFlag)
	if err != nil {
		t.Fatalf("autosave-status failed: %v\nstderr: %s", err, stderr)
	}

	for _, want := range []string{
		"Autosave:        on, every 3m; no save recorded yet\n",
		"Snapshots:       none saved yet\n",
		"Before shutdown: ",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status output is missing %q:\n%s", want, stdout)
		}
	}

	for _, internal := range []string{"reason:", "Inhibitors", "Phase", "Recorded"} {
		if strings.Contains(stdout, internal) {
			t.Fatalf("status output must not show %q:\n%s", internal, stdout)
		}
	}
}
