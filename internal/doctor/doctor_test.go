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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestRunReportsEveryCheckInGroupOrder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	units := filepath.Join(root, "units")
	if err := os.Mkdir(units, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(units, "sesh.service"), []byte("[Service]\nExecStart=sesh doctor\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		SnapshotDir:     filepath.Join(root, "snapshots"),
		HistoryPath:     filepath.Join(root, "history.json"),
		LogPath:         filepath.Join(root, "sesh.log"),
		ServiceDirs:     []string{units},
		AutosaveEnabled: true,
		Daemon:          doctor.Daemon{State: doctor.DaemonAbsent},
		CustomFields:    []doctor.CustomField{{Name: "weather", Command: ""}},
		ValidateCommand: func([]string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)

		if group, _, _ := strings.Cut(result.ID, "."); !strings.EqualFold(result.Group, group) {
			t.Errorf("%s is listed under group %q", result.ID, result.Group)
		}

		if (result.Status == doctor.StatusWarn || result.Status == doctor.StatusFail) && result.Fix == nil {
			t.Errorf("%s %s has no fix: %+v", result.ID, result.Status, result)
		}
	}

	want := []string{
		doctor.CheckInstallPath, doctor.CheckConfigFile, doctor.CheckTmuxClient, doctor.CheckTmuxServer, doctor.FieldCheck("weather"),
		doctor.CheckSnapshotRepository, doctor.CheckSnapshotCurrent, doctor.CheckActiveRestores, doctor.CheckHistoryFile,
		doctor.CheckLogFile,
	}
	if runtime.GOOS == "linux" {
		want = append(want, doctor.CheckServiceUnits, doctor.CheckServiceOOMPolicy, doctor.CheckAutosaveDaemon)
	}

	want = append(want, doctor.CheckSmokeSessions)
	if !slices.Equal(ids, want) {
		t.Fatalf("checks = %q, want %q", ids, want)
	}
}

func TestRunOmitsUnusedServiceAndCoordinatorChecks(test *testing.T) {
	test.Parallel()

	for _, dirs := range [][]string{nil, {test.TempDir()}} {
		results, err := doctor.Run(test.Context(), doctor.Options{
			ServiceDirs:     dirs,
			Daemon:          doctor.Daemon{State: doctor.DaemonAbsent},
			ValidateCommand: func([]string) error { return nil },
		})
		if err != nil {
			test.Fatal(err)
		}

		for _, id := range []string{doctor.CheckServiceUnits, doctor.CheckAutosaveDaemon} {
			if result := findCheck(results, id); result.ID != "" {
				test.Errorf("unused check %s was collected: %+v", id, result)
			}
		}

		if dirs == nil || runtime.GOOS != "linux" {
			if result := findCheck(results, doctor.CheckServiceOOMPolicy); result.ID != "" {
				test.Errorf("unsupported service OOM check was collected: %+v", result)
			}
		} else if result := findCheck(results, doctor.CheckServiceOOMPolicy); result.Status != doctor.StatusSkip || result.Needs != doctor.CheckTmuxServer {
			test.Errorf("applicable OOM check lost its prerequisite skip: %+v", result)
		}
	}
}

func TestCustomFieldChecksReportMissingCommands(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "weather")

	results, err := doctor.Run(t.Context(), doctor.Options{
		PathEnv: os.Getenv("PATH"),
		CustomFields: []doctor.CustomField{
			{Name: "sha", Command: ""},
			{Name: "today", Command: "date"},
			{Name: "weather", Command: missing},
		},
	})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	if sha := findCheck(results, doctor.FieldCheck("sha")); sha.Status != doctor.StatusOK {
		t.Fatalf("shell command field = %#v", sha)
	}

	if today := findCheck(results, doctor.FieldCheck("today")); today.Status != doctor.StatusOK || !strings.HasSuffix(today.Message, "/date") {
		t.Fatalf("command on PATH = %#v", today)
	}

	weather := findCheck(results, doctor.FieldCheck("weather"))
	if weather.Status != doctor.StatusFail || weather.Reason != doctor.ReasonNotFound ||
		weather.Fix == nil || !strings.Contains(weather.Fix.Manual, "display.fields.weather.command") {
		t.Fatalf("missing command result = %#v", weather)
	}
}

func TestBrokenConfigFailsItsCheckAndSkipsTheFieldsItDefines(t *testing.T) {
	t.Parallel()

	results, err := doctor.Run(t.Context(), doctor.Options{
		ConfigFiles: []doctor.ConfigFile{
			{Path: "/etc/sesh/config.toml", Status: doctor.ConfigNotFound},
			{Path: "/home/user/.config/sesh/config.toml", Status: doctor.ConfigNotLoaded},
		},
		ConfigError: "Invalid configuration at line 3, column 1",
	})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	config := findCheck(results, doctor.CheckConfigFile)
	if config.Status != doctor.StatusFail || config.Message != "Invalid configuration at line 3, column 1" ||
		config.Fix == nil || config.Fix.Manual != "Correct the error in /home/user/.config/sesh/config.toml" {
		t.Fatalf("config check = %#v", config)
	}

	if fields := findCheck(results, doctor.CheckFields); fields.Status != doctor.StatusSkip || fields.Needs != doctor.CheckConfigFile {
		t.Fatalf("fields check = %#v, want a skip that needs %s", fields, doctor.CheckConfigFile)
	}
}

func TestInstallPathListsEveryCopyOnPathOnce(t *testing.T) {
	t.Parallel()

	first, second, third := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(dir, "sesh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Symlink(filepath.Join(first, "sesh"), filepath.Join(third, "sesh")); err != nil {
		t.Fatal(err)
	}

	install := func(dirs ...string) doctor.Result {
		results, err := doctor.Run(t.Context(), doctor.Options{PathEnv: strings.Join(dirs, string(os.PathListSeparator))})
		if err != nil {
			t.Fatal(err)
		}

		return findCheck(results, doctor.CheckInstallPath)
	}

	if result := install(first, third); result.Status != doctor.StatusOK {
		t.Fatalf("a link to the same file is one copy: %#v", result)
	}

	if result := install(t.TempDir()); result.Status != doctor.StatusInfo {
		t.Fatalf("sesh missing from PATH = %#v", result)
	}

	result := install(second, third, first)
	if result.Status != doctor.StatusWarn || result.Fix == nil ||
		!slices.Equal(result.Details, []string{filepath.Join(second, "sesh"), filepath.Join(third, "sesh")}) {
		t.Fatalf("copies must be listed in PATH order: %#v", result)
	}
}

func TestLogFileCheckNeedsAWritableLocation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	existing := filepath.Join(dir, "sesh.log")

	if err := os.WriteFile(existing, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	readOnly := filepath.Join(dir, "locked")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]doctor.Status{
		existing: doctor.StatusOK,
		filepath.Join(dir, "state", "sesh", "a.log"): doctor.StatusOK,
		filepath.Join(readOnly, "sesh", "a.log"):     doctor.StatusFail,
		filepath.Join(existing, "a.log"):             doctor.StatusFail,
	} {
		results, err := doctor.Run(t.Context(), doctor.Options{LogPath: path})
		if err != nil {
			t.Fatal(err)
		}

		if result := findCheck(results, doctor.CheckLogFile); result.Status != want || (want == doctor.StatusFail && result.Fix == nil) {
			t.Errorf("log %s = %#v, want %s", path, result, want)
		}

		if _, err := os.Stat(filepath.Join(dir, "state")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the check created the log directory: %v", err)
		}
	}
}

func TestFixCommandsRepeatTheDoctorsFlags(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "my repo")

	r, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "catalog.json"), []byte("NOT_JSON"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		SnapshotDir: repoDir,
		Flags:       map[string]string{"--config": "/srv/sesh.toml", "--socket": "/tmp/s", "--repository": repoDir},
	})
	if err != nil {
		t.Fatal(err)
	}

	repo := findCheck(results, doctor.CheckSnapshotRepository)

	want := "sesh snapshot verify --config /srv/sesh.toml --repository '" + repoDir + "'"
	if repo.Status != doctor.StatusFail || repo.Fix == nil || repo.Fix.Command != want {
		t.Fatalf("repository fix = %+v, want command %q", repo.Fix, want)
	}
}

func TestResultJSONUsesReportFieldNames(t *testing.T) {
	t.Parallel()

	results, err := doctor.Run(t.Context(), doctor.Options{HistoryPath: filepath.Join(t.TempDir(), "history.json")})
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(findCheck(results, doctor.CheckHistoryFile))
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"id": "history.file", "group": "History", "status": "info", "reason": "not_found",
		"message": "Not created yet; switching sessions creates it", "fix": nil,
	}
	if !maps.Equal(parsed, want) {
		t.Fatalf("JSON = %s, want %v", data, want)
	}
}

func TestDoctorContextCancellation(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	results, err := doctor.Run(canceledCtx, doctor.Options{})
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
}

func TestDoctorContextDeadline(t *testing.T) {
	t.Parallel()

	deadlineCtx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := doctor.Run(deadlineCtx, doctor.Options{})
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
		SnapshotDir: nonExistent,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusInfo || repoRes.Reason != doctor.ReasonNotFound {
		t.Fatalf("expected not_found info, got %#v", repoRes)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusSkip || curRes.Needs != doctor.CheckSnapshotRepository {
		t.Fatalf("expected the current snapshot to skip on the missing repository, got %#v", curRes)
	}
}

func TestSnapshotRepository_Uninitialized(t *testing.T) {
	t.Parallel()

	emptyDir := testDir(t, "empty_repo")

	results, err := doctor.Run(t.Context(), doctor.Options{
		SnapshotDir: emptyDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusWarn || repoRes.Reason != doctor.ReasonUninitialized {
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
		SnapshotDir: symlinkPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusFail || repoRes.Reason != doctor.ReasonUnsafe {
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusFail || repoRes.Reason != doctor.ReasonRecoveryRequired {
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusFail || repoRes.Reason != doctor.ReasonCorrupt {
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusOK || repoRes.Message != "0 generations" {
		t.Fatalf("expected ok status with 0 generations, got %#v", repoRes)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusInfo || curRes.Reason != doctor.ReasonNotFound {
		t.Fatalf("expected not_found info for empty repo, got %#v", curRes)
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoRes := findCheck(results, doctor.CheckSnapshotRepository)
	if repoRes.Status != doctor.StatusOK || repoRes.Message != "1 generation" {
		t.Fatalf("expected ok with 1 generation, got %#v", repoRes)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusOK || !strings.HasSuffix(curRes.Message, "restores 1 session") {
		t.Fatalf("expected ok current snapshot, got %#v", curRes)
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarn || curRes.Reason != doctor.ReasonIncomplete {
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusOK || curRes.Fix != nil || !strings.HasSuffix(curRes.Message, "restores 1 session") {
		t.Fatalf("a restorable observed snapshot is healthy, got %#v", curRes)
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
	if curRes.Status != doctor.StatusWarn || curRes.Reason != doctor.ReasonSaveUnfinished {
		t.Fatalf("newer %s attempt did not report the newest saved generation: %#v", terminalState, curRes)
	}

	if !strings.Contains(curRes.Message, "the newest saved generation (1 session)") || curRes.Fix == nil {
		t.Fatalf("unfinished save result = %#v", curRes)
	}

	failed := strings.Contains(curRes.Message, "Latest save failed")
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
	if curRes.Status != doctor.StatusFail || curRes.Reason != doctor.ReasonCorrupt {
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusFail || curRes.Reason != doctor.ReasonCorrupt {
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusFail || curRes.Reason != doctor.ReasonCorrupt {
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
		SnapshotDir: repoDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	curRes := findCheck(results, doctor.CheckSnapshotCurrent)
	if curRes.Status != doctor.StatusWarn || curRes.Reason != doctor.ReasonSaveUnfinished {
		t.Fatalf("expected latest_save_unfinished warning after a pending save, got %#v", curRes)
	}
}

func TestHistoryState_MissingCleanFirstRun(t *testing.T) {
	t.Parallel()

	nonExistent := filepath.Join(t.TempDir(), "history.json")

	results, err := doctor.Run(t.Context(), doctor.Options{
		HistoryPath: nonExistent,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryFile)
	if historyRes.Status != doctor.StatusInfo || historyRes.Reason != doctor.ReasonNotFound {
		t.Fatalf("expected not_found info for clean first run, got %#v", historyRes)
	}
}

func TestHistoryState_Valid(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")
	historyPath := filepath.Join(historyDir, "history.json")

	validState := &session.State{
		Version: session.CurrentHistoryVersion,
		SwitchHistory: []session.SwitchHistoryEntry{
			{Session: session.Identity{Name: "dev"}, LastSwitchUnix: 12345},
		},
		RemoteSwitchHistory: []session.RemoteSwitchHistoryEntry{
			{Session: session.Identity{Name: "remote-box"}, LastSwitchUnix: 67890},
		},
		AgentViewHistory: []session.AgentViewHistoryEntry{
			{Session: session.Identity{Name: "agent-sess"}, PaneID: "%1", ViewedAtUnixNano: 11111},
		},
		LastAutosaveUnix: 0,
		PopupDisplayMode: "",
	}

	if err := session.SaveState(t.Context(), historyPath, validState); err != nil {
		t.Fatalf("saving valid state: %v", err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryFile)
	if historyRes.Status != doctor.StatusOK || historyRes.Reason != "" {
		t.Fatalf("expected ok status for valid history, got %#v", historyRes)
	}

	if !strings.Contains(historyRes.Message, "1 switch entry,") {
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
		HistoryPath: symlinkPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryFile)
	if historyRes.Status != doctor.StatusFail || historyRes.Reason != doctor.ReasonUnsafe {
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
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryFile)
	if historyRes.Status != doctor.StatusFail || historyRes.Reason != doctor.ReasonCorrupt {
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
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryFile)
	if historyRes.Status != doctor.StatusFail || historyRes.Reason != doctor.ReasonIncompatible {
		t.Fatalf("expected incompatible error for invalid version, got %#v", historyRes)
	}
}

func TestHistoryState_VersionOneFixConvertsFile(t *testing.T) {
	t.Parallel()

	historyDir := testDir(t, "history")
	historyPath := filepath.Join(historyDir, "history.json")

	versionOneJSON := `{"version": 1, "switchHistory": [], "remoteSwitchHistory": [], "agentViewHistory": [], "lastAutosaveTimestamp": 0, "popupDisplayMode": ""}`
	if err := os.WriteFile(historyPath, []byte(versionOneJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := doctor.Run(t.Context(), doctor.Options{HistoryPath: historyPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryFile)
	if historyRes.Status != doctor.StatusFail || historyRes.Reason != doctor.ReasonIncompatible || historyRes.Fix == nil {
		t.Fatalf("expected incompatible error with a fix, got %#v", historyRes)
	}

	if out, err := exec.CommandContext(t.Context(), "sh", "-c", historyRes.Fix.Command).CombinedOutput(); err != nil {
		t.Fatalf("fix %q failed: %v\n%s", historyRes.Fix.Command, err, out)
	}

	results, err = doctor.Run(t.Context(), doctor.Options{HistoryPath: historyPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if historyRes := findCheck(results, doctor.CheckHistoryFile); historyRes.Status != doctor.StatusOK {
		t.Fatalf("expected converted history to load, got %#v", historyRes)
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
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	historyRes := findCheck(results, doctor.CheckHistoryFile)
	if historyRes.Status != doctor.StatusFail || historyRes.Reason != doctor.ReasonCorrupt {
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
			{Session: session.Identity{Name: "important-project"}, LastSwitchUnix: 999999},
		},
		RemoteSwitchHistory: nil,
		AgentViewHistory:    nil,
		LastAutosaveUnix:    0,
		PopupDisplayMode:    "",
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

func findCheck(results []doctor.Result, id string) doctor.Result {
	for _, r := range results {
		if r.ID == id {
			return r
		}
	}

	return doctor.Result{}
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
