package cmdautosave

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

func TestAutosaveCommandHelp(t *testing.T) {
	t.Parallel()

	subcommands := []string{"final-save", "ensure-default-session"}

	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			root := newRootCommand(t.Context())

			stdout, stderr, err := executeCommand(root, "autosave", sub, "--help")
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

	actions := []string{"autosave-status", "autosave-save-if-due", "autosave-restore-on-start"}

	for _, actionName := range actions {
		t.Run(actionName, func(t *testing.T) {
			root := newRootCommand(t.Context())

			stdout, stderr, err := executeCommand(root, actionCmd, actionName, "--help")
			if err != nil {
				t.Fatalf("action %s --help failed: %v\nstderr: %s", actionName, err, stderr)
			}

			if !strings.Contains(stdout, actionName) {
				t.Fatalf("stdout missing %s in help: %s", actionName, stdout)
			}
		})
	}
}

func isolatedAutosaveHome(t *testing.T) {
	t.Helper()

	t.Setenv("TMUX", "")

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
}

func TestAutosaveStatusReportsDueStateWithDefaults(t *testing.T) {
	isolatedAutosaveHome(t)

	exec := executeJSON(t, actionCmd, "autosave-status", testNoConfigFlag, testJSONFlag)
	if !exec.envelope.OK {
		t.Fatalf("autosave status failed: %s", exec.stderr)
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)

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

	for _, action := range []string{"autosave-status", "autosave-save-if-due"} {
		t.Run(action, func(t *testing.T) {
			exec := executeJSON(t, actionCmd, action, testConfigFlag, cfgPath, testJSONFlag)
			if !exec.envelope.OK {
				t.Fatalf("disabled autosave %s must not fail: %s", action, exec.stderr)
			}

			result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
			if result.Status != "disabled" || result.Reason != "disabled" {
				t.Fatalf("result = %#v, want disabled/disabled", result)
			}
		})
	}
}

func TestAutosaveSaveIfDueSkipsWhenDefaultTmuxServerIsNotRunning(t *testing.T) {
	isolatedAutosaveHome(t)

	exec := executeJSON(t, actionCmd, "autosave-save-if-due", testNoConfigFlag, testJSONFlag)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("save-if-due without a running tmux server failed: %v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
	if result.Status != autosaveStatusSkipped || result.Reason != autosaveReasonTmuxNotRunning || result.Recorded {
		t.Fatalf("result = %#v, want an unrecorded skip because tmux is not running", result)
	}

	if result.Attempt != "" || result.Generation != "" {
		t.Fatalf("result = %#v, want no save attempt without a tmux server", result)
	}
}

func writeCorruptHistoryState(t *testing.T) {
	t.Helper()

	stateHome := filepath.Clean(os.Getenv("XDG_STATE_HOME"))
	path := filepath.Join(stateHome, "sesh", "history.json")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAutosaveHistoryDistinguishesMissingFromUnreadable(t *testing.T) {
	isolatedAutosaveHome(t)

	path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "sesh", "history.json")

	last, err := loadAutosaveHistory(path)
	if err != nil || last != 0 {
		t.Fatalf("missing history = (%d, %v), want (0, nil)", last, err)
	}

	if _, err := session.UpdateState(t.Context(), path, func(state *session.State) {
		state.LastAutosaveTimestamp = 1_700_000_000
	}); err != nil {
		t.Fatalf("recording history failed: %v", err)
	}

	last, err = loadAutosaveHistory(path)
	if err != nil || last != 1_700_000_000 {
		t.Fatalf("recorded history = (%d, %v), want the recorded timestamp", last, err)
	}

	writeCorruptHistoryState(t)

	if _, err := loadAutosaveHistory(path); err == nil {
		t.Fatal("an unreadable history file must report an error instead of a zero timestamp")
	}
}

func TestAutosaveStatusReportsHistoryUnavailable(t *testing.T) {
	isolatedAutosaveHome(t)
	writeCorruptHistoryState(t)

	status := executeJSON(t, actionCmd, "autosave-status", testNoConfigFlag, testJSONFlag)
	if status.err == nil {
		t.Fatal("autosave status with an unreadable history must report an error")
	}

	if clibase.ExitCode(status.err) != clibase.ExitCodeGeneral {
		t.Fatalf("status exit code = %d, want %d", clibase.ExitCode(status.err), clibase.ExitCodeGeneral)
	}

	if status.envelope.Error == nil {
		t.Fatal("degraded status must carry an error payload")
	}

	degraded := decodeJSONPayload[autosaveResultJSON](t, status.envelope.Result)
	if degraded.Status != "degraded" || degraded.Reason != automation.ReasonHistoryUnavailable {
		t.Fatalf("status result = %#v, want degraded/history_unavailable", degraded)
	}

	if degraded.HistoryState != "unavailable" {
		t.Fatalf("historyState = %q, want unavailable", degraded.HistoryState)
	}
}

func TestAutosaveSaveIfDueRefusesWhenHistoryUnavailable(t *testing.T) {
	isolatedAutosaveHome(t)
	writeCorruptHistoryState(t)

	save := executeJSON(t, actionCmd, "autosave-save-if-due", testNoConfigFlag, testJSONFlag)
	if save.err == nil {
		t.Fatal("save-if-due with an unreadable history must refuse")
	}

	if clibase.ExitCode(save.err) != clibase.ExitCodeGeneral {
		t.Fatalf("save exit code = %d, want %d", clibase.ExitCode(save.err), clibase.ExitCodeGeneral)
	}

	refusal := decodeJSONPayload[autosaveResultJSON](t, save.envelope.Result)
	if refusal.Status != "degraded" || refusal.Reason != automation.ReasonHistoryUnavailable {
		t.Fatalf("save result = %#v, want degraded/history_unavailable", refusal)
	}

	if refusal.Generation != "" || refusal.Recorded {
		t.Fatalf("refusal = %#v, want no generation and no recorded completion", refusal)
	}
}

func TestAutosaveStatusReportsDurableState(t *testing.T) {
	isolatedAutosaveHome(t)

	exec := executeJSON(t, actionCmd, "autosave-status", testNoConfigFlag, testJSONFlag)
	if !exec.envelope.OK {
		t.Fatalf("autosave status failed: %s", exec.stderr)
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
	if result.HistoryState != "never_run" {
		t.Fatalf("historyState = %q, want never_run", result.HistoryState)
	}

	if result.RepositoryState != "missing" {
		t.Fatalf("repositoryState = %q, want missing", result.RepositoryState)
	}
}

func TestAutosaveFinalSaveRequiresPositiveDeadline(t *testing.T) {
	isolatedAutosaveHome(t)

	deadline := executeJSON(
		t, "autosave", "final-save",
		testNoConfigFlag, testJSONFlag,
		testSocketFlag, filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "sesh.sock"),
		"--deadline", "0s",
	)
	if deadline.err == nil || clibase.ExitCode(deadline.err) != clibase.ExitCodeUsage {
		t.Fatalf("final-save with a zero deadline = (%v, %d), want a usage error", deadline.err, clibase.ExitCode(deadline.err))
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
		t, "autosave", "final-save",
		testConfigFlag, cfgPath,
		testSocketFlag, socket,
		testJSONFlag,
	)

	if exec.envelope.OK {
		t.Fatal("expected final-save to fail for invalid environment key")
	}

	if exec.envelope.Error == nil || exec.envelope.Error.Code != "configurationFailed" {
		t.Fatalf("final-save error = %+v, want configurationFailed", exec.envelope.Error)
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

	if exec.err == nil {
		t.Fatal("expected save-if-due to fail for invalid environment key")
	}
}

func TestAutosaveStatusTextReportsIntervalInMinutes(t *testing.T) {
	isolatedAutosaveHome(t)

	stdout, stderr, err := executeCommand(newRootCommand(t.Context()), actionCmd, "autosave-status", testNoConfigFlag)
	if err != nil {
		t.Fatalf("autosave-status failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Interval:   3m\n") {
		t.Fatalf("status output does not report the default 3 minute interval:\n%s", stdout)
	}
}
