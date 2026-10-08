//go:build linux || darwin

package doctor_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestResultSeverityHelpers(t *testing.T) {
	t.Parallel()

	okResults := []doctor.Result{
		{Check: doctor.CheckTmux, Status: doctor.StatusOK, Reason: "", Message: "ok", Fix: ""},
	}
	if doctor.HasErrors(okResults) {
		t.Fatal("expected no errors for ok results")
	}

	if doctor.HasWarnings(okResults) {
		t.Fatal("expected no warnings for ok results")
	}

	warnResults := []doctor.Result{
		{Check: doctor.CheckTmux, Status: doctor.StatusOK, Reason: "", Message: "ok", Fix: ""},
		{Check: doctor.CheckAutosave, Status: doctor.StatusWarning, Reason: doctor.ReasonIncomplete, Message: "warn", Fix: ""},
	}
	if doctor.HasErrors(warnResults) {
		t.Fatal("expected no errors for warn results")
	}

	if !doctor.HasWarnings(warnResults) {
		t.Fatal("expected warnings for warn results")
	}

	errResults := []doctor.Result{
		{Check: doctor.CheckTmux, Status: doctor.StatusError, Reason: doctor.ReasonUnavailable, Message: "err", Fix: ""},
	}
	if !doctor.HasErrors(errResults) {
		t.Fatal("expected errors for err results")
	}
}

func TestNilOptionsOrderAndDefaults(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	results, err := doctor.Run(ctx, doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	expectedChecks := []struct {
		check  string
		reason string
	}{
		{doctor.CheckTmux, doctor.ReasonNotConfigured},
		{doctor.CheckSnapshotRepository, doctor.ReasonNotConfigured},
		{doctor.CheckSnapshotCurrent, doctor.ReasonNotConfigured},
		{doctor.CheckHistoryState, doctor.ReasonNotConfigured},
		{doctor.CheckCaptureGuarantee, doctor.ReasonObservational},
		{doctor.CheckRestoreCapability, doctor.ReasonIncomplete},
		{doctor.CheckDaemonRuntime, doctor.ReasonNotConfigured},
		{doctor.CheckAutosave, doctor.ReasonIncomplete},
	}

	if len(results) != len(expectedChecks) {
		t.Fatalf("expected %d results, got %d", len(expectedChecks), len(results))
	}

	for i, expected := range expectedChecks {
		if results[i].Check != expected.check {
			t.Fatalf("result[%d] check = %q, want %q", i, results[i].Check, expected.check)
		}

		if results[i].Reason != expected.reason || results[i].Status != doctor.StatusWarning {
			t.Fatalf("result[%d] expected %q warning, got %#v", i, expected.reason, results[i])
		}
	}
}

func TestCustomFieldChecksReportMissingCommands(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "weather")

	results, err := doctor.Run(t.Context(), doctor.Options{
		CustomFields: []doctor.CustomField{
			{Name: "sha", Command: ""},
			{Name: "today", Command: "date"},
			{Name: "weather", Command: missing},
		},
	})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	var fields []doctor.Result

	for _, result := range results {
		if result.Check == doctor.CheckCustomField {
			fields = append(fields, result)
		}
	}

	if len(fields) != 3 || fields[0].Status != doctor.StatusOK || fields[1].Status != doctor.StatusOK {
		t.Fatalf("custom field results = %#v", fields)
	}

	if fields[2].Status != doctor.StatusError || fields[2].Reason != doctor.ReasonNotFound ||
		!strings.Contains(fields[2].Message, "custom.weather") || !strings.Contains(fields[2].Fix, "display.fields.weather.command") {
		t.Fatalf("missing command result = %#v", fields[2])
	}
}

func TestCaptureAndRestoreChecksDescribeImplementedBoundaries(t *testing.T) {
	t.Parallel()

	results, err := doctor.Run(t.Context(), doctor.Options{})
	if err != nil {
		t.Fatal(err)
	}

	capture := findCheck(results, doctor.CheckCaptureGuarantee)
	if capture.Reason != doctor.ReasonObservational ||
		!strings.Contains(capture.Message, "not a checkpoint") ||
		!strings.Contains(capture.Message, "shell jobs, pipelines and floating panes are not saved") ||
		!strings.Contains(capture.Message, "scrollback is kept but not replayed") {
		t.Fatalf("capture capability is inaccurate: %#v", capture)
	}

	restore := findCheck(results, doctor.CheckRestoreCapability)
	if restore.Reason != doctor.ReasonIncomplete ||
		!strings.Contains(restore.Message, "relaunches or resumes supported programs") ||
		!strings.Contains(restore.Message, "cannot confirm a program recovered") ||
		!strings.Contains(restore.Message, "scrollback is not replayed") {
		t.Fatalf("restore capability is inaccurate: %#v", restore)
	}
}

func TestAutosaveCapabilityDescribesCurrentPlatform(t *testing.T) {
	t.Parallel()

	results, err := doctor.Run(t.Context(), doctor.Options{})
	if err != nil {
		t.Fatal(err)
	}

	autosave := findCheck(results, doctor.CheckAutosave)
	if autosave.Reason != doctor.ReasonIncomplete {
		t.Fatalf("autosave capability is inaccurate: %#v", autosave)
	}

	requireMessageContains(t, autosave, "saves run on a timer and before shutdown")

	if runtime.GOOS != "linux" {
		return
	}

	requireMessageContains(t, autosave,
		"before shutdown and sleep",
		"a save cut short by shutdown is recorded as incomplete",
		"restore uses the newest finished save",
	)

	if strings.Contains(autosave.Message, "macOS") {
		t.Fatalf("autosave capability must describe Linux limits on Linux: %#v", autosave)
	}
}

func TestDoctorContextCancellation(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	results, err := doctor.Run(canceledCtx, doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: "",
	})
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled cause, got %v", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if oopsErr.Code() != "operation_canceled" {
		t.Fatalf("expected oops code operation_canceled, got %q", oopsErr.Code())
	}

	if results != nil {
		t.Fatalf("expected nil results on pre-run cancellation, got %v", results)
	}

	owner, ok := err.(interface{ OutcomeLogged() bool })
	if !ok || !owner.OutcomeLogged() {
		t.Fatalf("terminal doctor error does not retain log ownership: %T", err)
	}
}

func TestDoctorContextDeadline(t *testing.T) {
	t.Parallel()

	deadlineCtx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := doctor.Run(deadlineCtx, doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: "",
	})
	if err == nil {
		t.Fatal("expected error on expired deadline, got nil")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded cause, got %v", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if oopsErr.Code() != "operation_timed_out" {
		t.Fatalf("expected oops code operation_timed_out, got %q", oopsErr.Code())
	}
}

func TestSnapshotRepository_NotFound(t *testing.T) {
	t.Parallel()

	nonExistent := filepath.Join(t.TempDir(), "does-not-exist")

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: nonExistent,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusWarning || repoRes.Reason != doctor.ReasonNotFound {
		t.Fatalf("expected not_found warning, got %#v", repoRes)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarning || curRes.Reason != doctor.ReasonNotFound {
		t.Fatalf("expected not_found warning for current snapshot, got %#v", curRes)
	}
}

func TestSnapshotRepository_Uninitialized(t *testing.T) {
	t.Parallel()

	emptyDir := testDir(t, "empty_repo")

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: emptyDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusWarning || repoRes.Reason != doctor.ReasonUninitialized {
		t.Fatalf("expected uninitialized warning, got %#v", repoRes)
	}
}

func TestSnapshotRepository_UnsafeSymlink(t *testing.T) {
	t.Parallel()

	realDir := testDir(t, "real_repo")
	symlinkParent := testDir(t, "symlink_parent")

	symlinkPath := filepath.Join(symlinkParent, "symlink_repo")
	if err := os.Symlink(realDir, symlinkPath); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: symlinkPath,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusError || repoRes.Reason != doctor.ReasonUnsafe {
		t.Fatalf("expected unsafe error for symlink, got %#v", repoRes)
	}

	target, err := os.Readlink(symlinkPath)
	if err != nil || target != realDir {
		t.Fatalf("symlink target was mutated: %v, target=%q", err, target)
	}
}

func TestSnapshotRepository_InterruptedInitBarrier(t *testing.T) {
	t.Parallel()

	repoDir := testDir(t, "repo")

	markerPath := filepath.Join(repoDir, "init.marker")
	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusError || repoRes.Reason != doctor.ReasonRecoveryRequired {
		t.Fatalf("expected recovery_required error, got %#v", repoRes)
	}

	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("init.marker was removed by doctor: %v", err)
	}
}

func TestSnapshotRepository_CorruptCatalog(t *testing.T) {
	t.Parallel()
	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	catalogPath := filepath.Join(repoDir, "catalog.json")
	if err := os.WriteFile(catalogPath, []byte("NOT_JSON"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusError || repoRes.Reason != doctor.ReasonCorrupt {
		t.Fatalf("expected corrupt error for catalog, got %#v", repoRes)
	}
}

func TestSnapshotRepository_EmptyCommittedState(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusOK || !strings.Contains(repoRes.Message, "0 generations") {
		t.Fatalf("expected ok status with 0 generations, got %#v", repoRes)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarning || curRes.Reason != doctor.ReasonNotFound {
		t.Fatalf("expected not_found warning for empty repo, got %#v", curRes)
	}
}

func TestSnapshotRepository_CompleteSnapshot(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	commitTestFixture(t, r, true)

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusOK || !strings.Contains(repoRes.Message, "1 generations") {
		t.Fatalf("expected ok with 1 generation, got %#v", repoRes)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusOK || !strings.Contains(curRes.Message, "verified") {
		t.Fatalf("expected ok verified current snapshot, got %#v", curRes)
	}
}

func TestSnapshotRepository_PartialSnapshot(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	commitTestFixture(t, r, false)

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarning || curRes.Reason != doctor.ReasonIncomplete {
		t.Fatalf("expected incomplete warning for partial snapshot, got %#v", curRes)
	}
}

func TestSnapshotRepository_PracticalObservationalSnapshot(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "observational")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	commitObservationalTestFixture(t, r)

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarning || curRes.Reason != doctor.ReasonObservational {
		t.Fatalf("expected observational warning for practical snapshot, got %#v", curRes)
	}

	if !strings.Contains(curRes.Message, "is restorable (1 sessions)") ||
		!strings.Contains(curRes.Message, "not a checkpoint") {
		t.Fatalf("expected practical recovery message, got %q", curRes.Message)
	}

	if curRes.Fix != "" {
		t.Fatalf("expected empty fix for practical recovery snapshot, got %q", curRes.Fix)
	}
}

func TestSnapshotRepository_UnfinishedSaveReportsNewestGeneration(t *testing.T) {
	t.Parallel()

	for _, terminalState := range []string{"pending", "failed"} {
		t.Run(terminalState, func(t *testing.T) {
			t.Parallel()
			assertUnfinishedSaveReportsNewestGeneration(t, terminalState)
		})
	}
}

func assertUnfinishedSaveReportsNewestGeneration(t *testing.T, terminalState string) {
	t.Helper()

	repoDir := filepath.Join(t.TempDir(), "repository")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	commitObservationalTestFixture(t, r)

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if terminalState == "failed" {
		if err := r.Fail(t.Context(), attempt.ID); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{SnapshotDir: repoDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarning || curRes.Reason != doctor.ReasonSaveUnfinished {
		t.Fatalf("newer %s attempt did not report the newest saved generation: %#v", terminalState, curRes)
	}

	if !strings.Contains(curRes.Message, "the newest saved (1 sessions)") {
		t.Fatalf("unfinished save message = %q", curRes.Message)
	}

	failed := strings.Contains(curRes.Message, "latest save failed") && strings.Contains(curRes.Fix, "--json")
	if failed != (terminalState == "failed") {
		t.Fatalf("newer %s attempt reported the wrong unfinished-save cause: %#v", terminalState, curRes)
	}
}

func TestSnapshotRepository_CorruptPracticalGenerationArtifact(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repository")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	gen := commitObservationalTestFixture(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	artifactFile := filepath.Join(repoDir, "generations", gen.ID, "artifacts", "a0.bin.gz")
	if err := os.WriteFile(artifactFile, []byte("NOT_A_VALID_GZIP_FILE"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{SnapshotDir: repoDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusError || curRes.Reason != doctor.ReasonCorrupt {
		t.Fatalf("corrupt practical artifact was downgraded: %#v", curRes)
	}
}

func TestSnapshotRepository_CorruptManifest(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	gen := commitTestFixture(t, r, true)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(repoDir, "generations", gen.ID, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte("CORRUPT_MANIFEST_BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusError || curRes.Reason != doctor.ReasonCorrupt {
		t.Fatalf("expected corrupt error on corrupt manifest, got %#v", curRes)
	}
}

func TestSnapshotRepository_CorruptArtifact(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	gen := commitTestFixture(t, r, true)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	artifactFile := filepath.Join(repoDir, "generations", gen.ID, "artifacts", "a0.bin.gz")
	if err := os.WriteFile(artifactFile, []byte("NOT_A_VALID_GZIP_FILE"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusError || curRes.Reason != doctor.ReasonCorrupt {
		t.Fatalf("expected corrupt error on corrupt artifact, got %#v", curRes)
	}
}

func TestSnapshotRepository_PendingSaveAfterCompleteGenerationReportsIt(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	commitTestFixture(t, r, true)

	if _, err := r.Begin(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: repoDir,
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarning || curRes.Reason != doctor.ReasonSaveUnfinished {
		t.Fatalf("expected latest_save_unfinished warning after a pending save, got %#v", curRes)
	}
}

func TestHistoryState_MissingCleanFirstRun(t *testing.T) {
	t.Parallel()

	nonExistent := filepath.Join(t.TempDir(), "history.json")

	results, err := doctor.Run(t.Context(), doctor.Options{
		SnapshotDir: "",
		HistoryPath: nonExistent,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryState)
	if historyRes.Status != doctor.StatusWarning || historyRes.Reason != doctor.ReasonNotFound {
		t.Fatalf("expected not_found warning for clean first run, got %#v", historyRes)
	}

	if !strings.Contains(historyRes.Message, "first-run clean state") {
		t.Fatalf("expected first-run message, got %q", historyRes.Message)
	}
}

func TestHistoryState_Valid(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")
	historyPath := filepath.Join(historyDir, "history.json")

	validState := &session.State{
		Version: session.CurrentHistoryVersion,
		SwitchHistory: []session.SwitchHistoryEntry{
			{Session: session.Identity{Name: "dev"}, LastSwitch: 12345},
		},
		RemoteSwitchHistory: []session.RemoteSwitchHistoryEntry{
			{Session: session.Identity{Name: "remote-box"}, LastSwitch: 67890},
		},
		AgentViewHistory: []session.AgentViewHistoryEntry{
			{Session: session.Identity{Name: "agent-sess"}, PaneID: "%1", ViewedAt: 11111},
		},
		LastAutosaveTimestamp: 0,
		PopupDisplayMode:      "",
	}

	if err := session.SaveState(t.Context(), historyPath, validState); err != nil {
		t.Fatalf("saving valid state: %v", err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryState)
	if historyRes.Status != doctor.StatusOK || historyRes.Reason != "" {
		t.Fatalf("expected ok status for valid history, got %#v", historyRes)
	}

	if !strings.Contains(historyRes.Message, "1 switch entries") {
		t.Fatalf("expected switch count in message, got %q", historyRes.Message)
	}
}

func TestHistoryState_UnsafeSymlink(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")

	targetPath := filepath.Join(historyDir, "real_history.json")
	if err := os.WriteFile(targetPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkParent := testDir(t, "symlink_parent")

	symlinkPath := filepath.Join(symlinkParent, "symlink_history.json")
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: symlinkPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryState)
	if historyRes.Status != doctor.StatusError || historyRes.Reason != doctor.ReasonUnsafe {
		t.Fatalf("expected unsafe error for history symlink, got %#v", historyRes)
	}

	dest, err := os.Readlink(symlinkPath)
	if err != nil || dest != targetPath {
		t.Fatalf("symlink mutated: %v, dest=%q", err, dest)
	}
}

func TestHistoryState_CorruptJSON(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")

	historyPath := filepath.Join(historyDir, "history.json")
	if err := os.WriteFile(historyPath, []byte("{CORRUPT_JSON"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryState)
	if historyRes.Status != doctor.StatusError || historyRes.Reason != doctor.ReasonCorrupt {
		t.Fatalf("expected corrupt error for invalid JSON, got %#v", historyRes)
	}
}

func TestHistoryState_InvalidVersion(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")
	historyPath := filepath.Join(historyDir, "history.json")

	invalidVersionJSON := `{"version": 999, "switchHistory": [], "remoteSwitchHistory": [], "agentViewHistory": [], "lastAutosaveTimestamp": 0, "popupDisplayMode": ""}`
	if err := os.WriteFile(historyPath, []byte(invalidVersionJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryState)
	if historyRes.Status != doctor.StatusError || historyRes.Reason != doctor.ReasonIncompatible {
		t.Fatalf("expected incompatible error for invalid version, got %#v", historyRes)
	}
}

func TestHistoryState_Oversized(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")
	historyPath := filepath.Join(historyDir, "history.json")

	oversized := make([]byte, 1024*1024+1)
	if err := os.WriteFile(historyPath, oversized, 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryState)
	if historyRes.Status != doctor.StatusError || historyRes.Reason != doctor.ReasonCorrupt {
		t.Fatalf("expected corrupt error for oversized file, got %#v", historyRes)
	}
}

func TestHistoryState_BytesUntouched(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")
	historyPath := filepath.Join(historyDir, "history.json")

	validState := &session.State{
		Version: session.CurrentHistoryVersion,
		SwitchHistory: []session.SwitchHistoryEntry{
			{Session: session.Identity{Name: "important-project"}, LastSwitch: 999999},
		},
		RemoteSwitchHistory:   nil,
		AgentViewHistory:      nil,
		LastAutosaveTimestamp: 0,
		PopupDisplayMode:      "",
	}

	if err := session.SaveState(t.Context(), historyPath, validState); err != nil {
		t.Fatal(err)
	}

	beforeBytes, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	beforeHash := sha256.Sum256(beforeBytes)

	_, err = doctor.Run(t.Context(), doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	afterBytes, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	afterHash := sha256.Sum256(afterBytes)

	if beforeHash != afterHash {
		t.Fatal("doctor modified history state file bytes")
	}
}

func TestResultJSONSerialization(t *testing.T) {
	t.Parallel()

	res := doctor.Result{
		Check:   doctor.CheckTmux,
		Status:  doctor.StatusOK,
		Reason:  "",
		Message: "tmux 3.6 (server pid 42)",
		Fix:     "",
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if parsed["check"] != doctor.CheckTmux || parsed["status"] != doctor.StatusOK || parsed["message"] != "tmux 3.6 (server pid 42)" {
		t.Fatalf("unexpected JSON fields: %v", parsed)
	}
}

type schemaLevelCase struct {
	name          string
	opts          func(t *testing.T) doctor.Options
	cancelCtx     bool
	wantCompLevel string
	wantStatus    string
	wantReason    string
	wantRecords   int
	errorChecks   string
}

func TestLogging_SchemaAndLevels(t *testing.T) {
	t.Parallel()

	tests := []schemaLevelCase{
		{
			name: "ok_with_no_server_is_degraded_due_to_capability_warnings",
			opts: func(_ *testing.T) doctor.Options {
				return doctor.Options{Server: nil, SnapshotDir: "", HistoryPath: ""}
			},
			cancelCtx:     false,
			wantCompLevel: "warn",
			wantStatus:    "degraded",
			wantReason:    "checks_degraded",
			wantRecords:   2,
			errorChecks:   "",
		},
		{
			name: "failed_checks_level",
			opts: func(t *testing.T) doctor.Options {
				t.Helper()
				historyDir := testDir(t, "bad_history")
				badPath := filepath.Join(historyDir, "bad.json")
				_ = os.WriteFile(badPath, []byte("NOT_JSON"), 0o600)

				return doctor.Options{Server: nil, SnapshotDir: "", HistoryPath: badPath}
			},
			cancelCtx:     false,
			wantCompLevel: "error",
			wantStatus:    "failed",
			wantReason:    "checks_failed",
			wantRecords:   2,
			errorChecks:   doctor.CheckHistoryState,
		},
		{
			name: "operational_cancellation_level",
			opts: func(_ *testing.T) doctor.Options {
				return doctor.Options{Server: nil, SnapshotDir: "", HistoryPath: ""}
			},
			cancelCtx:     true,
			wantCompLevel: "error",
			wantStatus:    "failed",
			wantReason:    "operation_canceled",
			wantRecords:   1,
			errorChecks:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runSchemaLevelTest(t, tt)
		})
	}
}

func runSchemaLevelTest(t *testing.T, tt schemaLevelCase) {
	t.Helper()

	var logBuf bytes.Buffer

	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(t.Context())

	if tt.cancelCtx {
		var cancel context.CancelFunc

		ctx, cancel = context.WithCancel(ctx)
		cancel()
	}

	opts := tt.opts(t)
	_, _ = doctor.Run(ctx, opts)

	logLines := bytes.Split(bytes.TrimSpace(logBuf.Bytes()), []byte{'\n'})
	if len(logLines) != tt.wantRecords {
		t.Fatalf("log record count = %d, want %d: %s", len(logLines), tt.wantRecords, logBuf.Bytes())
	}

	lastLine := logLines[len(logLines)-1]

	var lastRecord map[string]any
	if err := json.Unmarshal(lastLine, &lastRecord); err != nil {
		t.Fatalf("invalid JSON log: %v, raw: %s", err, string(lastLine))
	}

	if lastRecord["component"] != "doctor" || lastRecord["operation"] != "run" {
		t.Fatalf("unexpected component/operation: %v", lastRecord)
	}

	if lastRecord["level"] != tt.wantCompLevel {
		t.Fatalf("expected level %q, got %q", tt.wantCompLevel, lastRecord["level"])
	}

	if lastRecord["status"] != tt.wantStatus {
		t.Fatalf("expected status %q, got %q", tt.wantStatus, lastRecord["status"])
	}

	if lastRecord["reason"] != tt.wantReason {
		t.Fatalf("expected reason %q, got %q", tt.wantReason, lastRecord["reason"])
	}

	errorChecks, _ := lastRecord["error_checks"].(string)
	if errorChecks != tt.errorChecks {
		t.Fatalf("expected error_checks %q, got %q", tt.errorChecks, errorChecks)
	}
}

func TestLogging_Privacy(t *testing.T) {
	t.Parallel()

	const (
		secretPath     = "TOP_SECRET_PATH_XYZ_123"
		secretSession  = "SECRET_SESSION_NAME_999"
		secretArtifact = "SECRET_ARTIFACT_TOKEN_444"
	)

	dir := filepath.Join(t.TempDir(), secretPath)
	_ = os.MkdirAll(dir, 0o700)

	var logBuf bytes.Buffer

	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(t.Context())

	_, _ = doctor.Run(ctx, doctor.Options{
		Server:      nil,
		SnapshotDir: dir,
		HistoryPath: filepath.Join(dir, secretSession),
	})

	logStr := logBuf.String()
	for _, forbidden := range []string{secretPath, secretSession, secretArtifact} {
		if strings.Contains(logStr, forbidden) {
			t.Fatalf("privacy violation: log contains forbidden string %q:\n%s", forbidden, logStr)
		}
	}
}

func TestLogging_StdoutStderrIsolation(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer

	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(t.Context())

	_, err := doctor.Run(ctx, doctor.Options{
		Server:      nil,
		SnapshotDir: "",
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if logBuf.Len() == 0 {
		t.Fatal("expected logs in private sink buffer")
	}
}

func findCheck(results []doctor.Result, check string) doctor.Result {
	for _, r := range results {
		if r.Check == check {
			return r
		}
	}

	return doctor.Result{}
}

func requireMessageContains(t *testing.T, result doctor.Result, fragments ...string) {
	t.Helper()

	for _, fragment := range fragments {
		if !strings.Contains(result.Message, fragment) {
			t.Fatalf("%s message does not contain %q: %#v", result.Check, fragment, result)
		}
	}
}

func commitTestFixture(t *testing.T, r *store.Repository, complete bool) store.Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("synthetic terminal history content\n")
	hash := sha256.Sum256(content)

	m := model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  1,
		Finished: 2,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "v0.3.0",
			Consistency:    "observed",
		},
		Sessions: []model.Session{
			{ID: "s0", Name: model.Bytes("work"), Directory: model.Bytes("/tmp")},
		},
		Windows: []model.Window{
			{ID: "w0", Name: model.Bytes("main"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}},
		},
		Links: []model.Link{
			{Session: "s0", Window: "w0", Index: 0, Active: true},
		},
		Panes: []model.Pane{
			{ID: "p0", Window: "w0", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Terminal: model.Terminal{Artifact: "a0"}, Launch: model.Launch{Recovery: "unverified"}},
		},
		Artifacts: []model.Artifact{
			{ID: "a0", Kind: "terminal-text", Length: int64(len(content)), SHA256: hex.EncodeToString(hash[:])},
		},
		Evidence: nil,
	}

	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		status := "verified"
		reason := ""

		if !complete {
			status = "unverified"
			reason = "not_captured"
		}

		m.Evidence = append(m.Evidence, model.Evidence{Domain: domain, Status: status, Reason: reason})
	}

	if complete {
		m.Source.Consistency = "checkpoint"
	}

	artifacts := map[string]io.Reader{
		"a0": bytes.NewReader(content),
	}

	generation, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func commitObservationalTestFixture(t *testing.T, r *store.Repository) store.Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("synthetic terminal history content\n")
	hash := sha256.Sum256(content)

	m := model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  1,
		Finished: 2,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "v0.3.0",
			Consistency:    "observed",
		},
		Sessions: []model.Session{
			{ID: "s0", Name: model.Bytes("work"), Directory: model.Bytes("/tmp")},
		},
		Windows: []model.Window{
			{ID: "w0", Name: model.Bytes("main"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}},
		},
		Links: []model.Link{
			{Session: "s0", Window: "w0", Index: 0, Active: true},
		},
		Panes: []model.Pane{
			{
				ID: "p0", Window: "w0", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true,
				Terminal: model.Terminal{Artifact: "a0"},
				Launch: model.Launch{
					Executable: model.Bytes("/usr/bin/top"),
					Arguments:  []model.Bytes{model.Bytes("top")},
					Directory:  model.Bytes("/tmp"),
					Recovery:   model.RecoveryRestart,
					Reason:     "",
				},
			},
		},
		Artifacts: []model.Artifact{
			{ID: "a0", Kind: "terminal-text", Length: int64(len(content)), SHA256: hex.EncodeToString(hash[:])},
		},
		Evidence: []model.Evidence{
			{Domain: "S01", Status: "observed", Reason: "observed_only"},
			{Domain: "S02", Status: "observed", Reason: "observed_only"},
			{Domain: "S03", Status: "observed", Reason: "observed_only"},
			{Domain: "S04", Status: "observed", Reason: "observed_only"},
			{Domain: "S05", Status: "observed", Reason: "observed_only"},
			{Domain: "S06", Status: "observed", Reason: "terminal_checkpoint_unavailable"},
			{Domain: "S07", Status: "observed", Reason: "observed_only"},
			{Domain: "S08", Status: "unverified", Reason: "application_checkpoint_unavailable"},
			{Domain: "S09", Status: "observed", Reason: "observed_only"},
			{Domain: "S10", Status: "observed", Reason: "observed_only"},
		},
	}

	artifacts := map[string]io.Reader{
		"a0": bytes.NewReader(content),
	}

	generation, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func testDir(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return dir
}
