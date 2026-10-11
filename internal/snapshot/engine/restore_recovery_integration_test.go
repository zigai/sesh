//go:build (linux || darwin) && integration

package engine_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func commitCapture(t *testing.T, ctx context.Context, repoDir string, observation capture.Observation) string {
	t.Helper()

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	attempt, err := repo.Begin(ctx)
	if err != nil {
		t.Fatalf("repo.Begin failed: %v", err)
	}

	artifacts := make(map[string]io.Reader, len(observation.Artifacts))
	for id, data := range observation.Artifacts {
		artifacts[id] = bytes.NewReader(data)
	}

	generation, err := repo.Commit(ctx, attempt.ID, observation.Manifest, artifacts)
	if err != nil {
		t.Fatalf("repo.Commit failed: %v", err)
	}

	if err := repo.Close(); err != nil {
		t.Fatalf("repo.Close failed: %v", err)
	}

	return generation.ID
}

func observeSession(t *testing.T, ctx context.Context, source *gotmux.Server, repoDir string, session gotmux.Session) string {
	t.Helper()

	observation, err := capture.Observe(ctx, source, []gotmux.SessionID{session.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	return commitCapture(t, ctx, repoDir, observation)
}

func disableSourceWindowRename(t *testing.T, ctx context.Context, session gotmux.Session) {
	t.Helper()

	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("WindowInfos failed: %v", err)
	}

	for _, link := range links {
		options := link.Handle().Window().Options()

		if err := options.SetAutomaticRename(ctx, false); err != nil {
			t.Fatalf("disabling automatic rename failed: %v", err)
		}

		if err := options.SetAllowRename(ctx, false); err != nil {
			t.Fatalf("disabling window rename failed: %v", err)
		}
	}
}

func redirectPanesToMissingDirectory(t *testing.T, manifest *model.Manifest, sessionID, directory string) {
	t.Helper()

	windows := map[string]bool{}

	for _, link := range manifest.Links {
		if link.Session == sessionID {
			windows[link.Window] = true
		}
	}

	redirected := 0

	for index := range manifest.Panes {
		pane := &manifest.Panes[index]
		if !windows[pane.Window] {
			continue
		}

		pane.Directory = model.Bytes(directory)
		redirected++
	}

	if redirected == 0 {
		t.Fatal("no panes were redirected to the missing directory")
	}
}

// observeSessionAtMissingDirectory commits a generation whose panes point at a directory that
// does not exist, so a restore registers its operation and then fails after admission.
func observeSessionAtMissingDirectory(t *testing.T, ctx context.Context, source *gotmux.Server, repoDir string, session gotmux.Session) string {
	t.Helper()

	observation, err := capture.Observe(ctx, source, []gotmux.SessionID{session.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	redirectPanesToMissingDirectory(t, &observation.Manifest, observation.Manifest.Sessions[0].ID, filepath.Join(t.TempDir(), "missing"))

	if err := observation.Manifest.Validate(); err != nil {
		t.Fatalf("manifest validation failed: %v", err)
	}

	return commitCapture(t, ctx, repoDir, observation)
}

func openFixture(t *testing.T, ctx context.Context, repoDir string) *store.Repository {
	t.Helper()

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	t.Cleanup(func() { _ = repo.Close() })

	return repo
}

func TestRestoreRecoveryAPIsRejectForeignOperation(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "foreign-operation-source", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, live)

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := observeSession(t, ctx, server, repoDir, live)
	repo := openFixture(t, ctx, repoDir)

	operationID := store.NewOperationID()

	_, err = repo.RegisterOperation(ctx, operationID, generation, model.Bytes("foreign-target"), []store.Action{
		{ID: "launch", Kind: restore.KindLaunch, State: restore.StatePlanned},
	})
	if err != nil {
		t.Fatalf("RegisterOperation failed: %v", err)
	}

	if _, err := engine.ReconcileRestore(ctx, repo, operationID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ReconcileRestore must reject foreign operation, got %v", err)
	}

	if _, err := engine.AbandonRestore(ctx, repo, operationID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("AbandonRestore must reject foreign operation, got %v", err)
	}

	op, err := repo.ReconcileOperation(ctx, operationID)
	if err != nil {
		t.Fatalf("ReconcileOperation failed: %v", err)
	}

	if op.State != "active" {
		t.Fatalf("foreign operation state changed to %q", op.State)
	}
}

func TestEnginePartialRestoreKeepsPinUntilExplicitAbandon(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "pin-release", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, live)

	generation := observeSession(t, ctx, server, repoDir, live)
	newer := observeSession(t, ctx, server, repoDir, live)

	repo := openFixture(t, ctx, repoDir)

	if newer == generation {
		t.Fatalf("expected two distinct generations, got %q twice", generation)
	}

	if err := server.Environment().Set(ctx, "SESH_ENGINE_TARGET_ONLY", "target-only"); err != nil {
		t.Fatalf("setting target-only global environment failed: %v", err)
	}

	if err := live.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	result, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	assertPartialRestoreResult(t, result, err, "")

	assertMissingRequirementReason(t, result, "inherited_options_materialized_locally")
	assertMissingRequirementReason(t, result, "global_scope_environment_materialized_locally")
	assertMissingRequirementReason(t, result, "target_environment_masked_locally")

	assertPartialOperationVerified(t, ctx, repo, result.OperationID)

	if err := repo.Delete(ctx, generation); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("partial restore must protect its source, got %v", err)
	}

	resumed, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       result.OperationID,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	assertPartialRestoreResult(t, resumed, err, result.OperationID)
	assertReinspectedNoProcessPanes(t, resumed.Panes)

	assertMissingRequirementReason(t, resumed, "inherited_options_materialized_locally")
	assertMissingRequirementReason(t, resumed, "global_scope_environment_materialized_locally")

	if _, err := engine.AbandonRestore(ctx, repo, result.OperationID); err != nil {
		t.Fatalf("AbandonRestore failed: %v", err)
	}

	assertSourcePinReleased(t, ctx, repo, generation)
}

func assertReinspectedNoProcessPanes(t *testing.T, panes []restore.PaneResult) {
	t.Helper()

	if len(panes) == 0 {
		t.Fatal("reinspection omitted per-pane evidence")
	}

	for _, pane := range panes {
		if pane.RuntimeID == "" || pane.Outcome != restore.PaneOutcomeDefaultShell || pane.AppVerified || pane.CWDFallback {
			t.Fatalf("reinspected no-process pane result = %+v, want verified runtime identity and default-shell evidence", pane)
		}
	}
}

func TestEnginePracticalRestoreSettlesVerifiedFidelityLimits(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name: "practical-release", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, live)
	generation := observeSession(t, ctx, server, repoDir, live)
	_ = observeSession(t, ctx, server, repoDir, live)

	repo := openFixture(t, ctx, repoDir)
	if err := live.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	result, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot: leaseRoot, Generation: generation, Sessions: nil, OperationID: "",
		LaunchProcesses: false, MissingWorkingDir: restore.MissingWorkingDirFail,
		Profile: engine.RecoveryPractical,
	})
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if result.Status != "best_effort" || !result.Verified || len(result.MissingRequirements) == 0 {
		t.Fatalf("result = %+v, want verified best-effort with visible limits", result)
	}

	status, err := engine.ReconcileRestore(ctx, repo, result.OperationID)
	if err != nil {
		t.Fatalf("ReconcileRestore failed: %v", err)
	}

	if status.State != "complete" || !status.Settled {
		t.Fatalf("status = %+v, want settled operation", status)
	}

	assertSourcePinReleased(t, ctx, repo, generation)
}

func assertPartialRestoreResult(t *testing.T, result engine.RestoreResult, err error, operationID string) {
	t.Helper()

	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if result.Status != "partial" || len(result.MissingRequirements) == 0 {
		t.Fatalf("expected explicit partial restore, got %+v", result)
	}

	if operationID != "" && result.OperationID != operationID {
		t.Fatalf("restore operation identity changed: got %q want %q", result.OperationID, operationID)
	}
}

func assertMissingRequirementReason(t *testing.T, result engine.RestoreResult, reason string) {
	t.Helper()

	for _, requirement := range result.MissingRequirements {
		if string(requirement.Reason) == reason {
			return
		}
	}

	t.Fatalf("restore result omitted missing requirement %q: %+v", reason, result.MissingRequirements)
}

func TestEngineRestoreFailureRetainsPinAndSource(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	generation := setupTwoSessionSourceWithMissingDir(t, ctx, server, repoDir)
	repo := openFixture(t, ctx, repoDir)

	unrelated, unrelatedLinks, unrelatedPaneInfo := setupUnrelatedEngineSession(t, ctx, server)

	result := runFailingRestoreOnMissingDir(t, ctx, repo, server, leaseRoot, generation)

	assertRetainedAndUnrelatedIntact(t, ctx, server, result, unrelated, unrelatedLinks, unrelatedPaneInfo)
	assertFailureRetainedPinAndExplicitAbandonment(t, ctx, repo, server, leaseRoot, generation, result.OperationID)
}

func TestEngineRestoreRefusesDispatchedButUnconfirmedEffect(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "collision-source", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, live)

	generation := observeSessionAtMissingDirectory(t, ctx, server, repoDir, live)

	if err := live.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	repo := openFixture(t, ctx, repoDir)

	result, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if !errors.Is(err, restore.ErrWorkingDirNotFound) {
		t.Fatalf("expected ErrWorkingDirNotFound, got %v", err)
	}

	if result.OperationID == "" {
		t.Fatal("operation identity must survive an admission conflict")
	}

	plan := buildAllSessionsPlan(t, ctx, repo, generation)

	firstStep := plan.Steps[0]

	if err := repo.Transition(ctx, result.OperationID, restore.ActionID(firstStep.ID()), "planned", "issued", nil); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	_, err = engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       result.OperationID,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if !errors.Is(err, restore.ErrPriorUncertainty) {
		t.Fatalf("expected ErrPriorUncertainty, got %v", err)
	}

	if _, err := server.FindSession(ctx, "collision-source"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("refused retry must not create sessions, got %v", err)
	}
}

func TestEngineRestoreRefusesRestartedDaemonTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "restore.sock")

	config := gotmux.Config{
		SocketPath: socket,
		ConfigFile: "/dev/null",
		Env:        []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "SHELL=/bin/sh", "TERM=xterm-256color", "LC_ALL=C.UTF-8"},
		Dir:        dir,
	}

	first, err := gotmux.New(config)
	if err != nil {
		t.Fatalf("tmux.New failed: %v", err)
	}

	source, err := first.NewSession(ctx, gotmux.NewSessionOptions{Name: "restart-source", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyAllowStart})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	killDaemonOnCleanup(t, first, source.ServerIdentity())

	disableSourceWindowRename(t, ctx, source)

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	generation := observeSessionAtMissingDirectory(t, ctx, first, repoDir, source)

	if _, err := first.NewSession(ctx, gotmux.NewSessionOptions{Name: "restart-keepalive", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly}); err != nil {
		t.Fatalf("keepalive session failed: %v", err)
	}

	if err := source.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	repo := openFixture(t, ctx, repoDir)

	result, err := engine.Restore(ctx, repo, first, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if !errors.Is(err, restore.ErrWorkingDirNotFound) {
		t.Fatalf("expected ErrWorkingDirNotFound, got %v", err)
	}

	if result.OperationID == "" {
		t.Fatal("operation identity must survive an admission conflict")
	}

	if err := first.Kill(ctx); err != nil {
		t.Fatalf("Killing the daemon failed: %v", err)
	}

	second, err := gotmux.New(config)
	if err != nil {
		t.Fatalf("tmux.New failed: %v", err)
	}

	replacement, err := second.NewSession(ctx, gotmux.NewSessionOptions{Name: "restart-source", Start: gotmux.StartPolicyAllowStart})
	if err != nil {
		t.Fatalf("starting the replacement daemon failed: %v", err)
	}

	killDaemonOnCleanup(t, second, replacement.ServerIdentity())

	_, err = engine.Restore(ctx, repo, second, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       result.OperationID,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if err == nil {
		t.Fatal("expected resumption against a restarted daemon to be refused")
	}

	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected a target identity conflict, got %v", err)
	}
}

func TestSettleDeadRestoresAbandonsARestoreWhoseServerExited(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	server := tmuxtest.NewServer(t)
	repo, operationID := interruptRestoreAtMissingDirectory(t, ctx, server)

	if settled, err := engine.SettleDeadRestores(ctx, repo); err != nil || len(settled) != 0 {
		t.Fatalf("settle while the server runs = %d settled, %v; want the restore kept", len(settled), err)
	}

	probe, err := server.Probe(ctx)
	if err != nil {
		t.Fatalf("probe the target server: %v", err)
	}

	if err := server.Kill(ctx); err != nil {
		t.Fatalf("kill the target server: %v", err)
	}

	waitForProcessExit(t, ctx, probe.Identity.PID)

	settled, err := engine.SettleDeadRestores(ctx, repo)
	if err != nil {
		t.Fatalf("SettleDeadRestores failed: %v", err)
	}

	if len(settled) != 1 || settled[0].OperationID != operationID {
		t.Fatalf("settled = %+v, want the interrupted restore abandoned", settled)
	}

	active, err := engine.ActiveRestores(ctx, repo)
	if err != nil || len(active) != 0 {
		t.Fatalf("active restores after settling = %d, %v; want none", len(active), err)
	}
}

// interruptRestoreAtMissingDirectory leaves an admitted restore active against server, as a restore
// cut short by a shutdown does.
func interruptRestoreAtMissingDirectory(t *testing.T, ctx context.Context, server *gotmux.Server) (*store.Repository, string) {
	t.Helper()

	source, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "exited-source", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, source)

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := observeSessionAtMissingDirectory(t, ctx, server, repoDir, source)

	if err := source.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	repo := openFixture(t, ctx, repoDir)

	interrupted, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         filepath.Join(t.TempDir(), "leases"),
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if !errors.Is(err, restore.ErrWorkingDirNotFound) || interrupted.OperationID == "" {
		t.Fatalf("restore = %q, %v; want an admitted restore stopped by the missing directory", interrupted.OperationID, err)
	}

	return repo, interrupted.OperationID
}

// waitForProcessExit waits for a process to be gone, which kill-server does not wait for.
func waitForProcessExit(t *testing.T, ctx context.Context, pid int) {
	t.Helper()

	for syscall.Kill(pid, 0) == nil {
		select {
		case <-ctx.Done():
			t.Fatalf("process %d still runs: %v", pid, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// killDaemonOnCleanup stops a daemon the test started itself. The identity guard keeps the
// cleanup of a replaced daemon from killing its successor at the same socket.
func killDaemonOnCleanup(t *testing.T, server *gotmux.Server, identity gotmux.ServerIdentity) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := server.KillMatching(ctx, identity)
		if err != nil && !errors.Is(err, gotmux.ErrNoServer) && !errors.Is(err, gotmux.ErrServerChanged) {
			t.Errorf("cleanup kill daemon: %v", err)
		}
	})
}

func TestEngineRestoreLogSchema(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "log-source", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, live)

	generation := observeSession(t, ctx, server, repoDir, live)

	if err := live.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	var buffer bytes.Buffer

	logger := zerolog.New(&buffer)
	logCtx := logger.WithContext(ctx)

	repo := openFixture(t, ctx, repoDir)

	if _, err := engine.Restore(logCtx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	}); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	assertSuccessLogRecords(t, buffer.Bytes(), repoDir)

	buffer.Reset()
	runFailingRestoreForLogging(t, logCtx, repo, server, leaseRoot)
	assertFailureLogRecords(t, buffer.Bytes(), repoDir, buffer.String())
}

func decodeLogRecords(t *testing.T, data []byte) []map[string]any {
	t.Helper()

	records := make([]map[string]any, 0, 4)

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}

		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decoding log record failed: %v", err)
		}

		records = append(records, record)
	}

	return records
}

func assertPartialOperationVerified(t *testing.T, ctx context.Context, repo *store.Repository, operationID string) {
	t.Helper()

	status, err := engine.ReconcileRestore(ctx, repo, operationID)
	if err != nil {
		t.Fatalf("ReconcileRestore failed: %v", err)
	}

	if status.State != "active" || status.Settled || len(status.Uncertain) != 0 {
		t.Fatalf("expected an active structurally verified partial operation, got %+v", status)
	}

	for _, action := range status.Actions {
		if action.State != "verified" {
			t.Fatalf("expected every action verified, got %+v", action)
		}
	}
}

func assertSourcePinReleased(t *testing.T, ctx context.Context, repo *store.Repository, generation string) {
	t.Helper()

	if err := repo.Delete(ctx, generation); err != nil {
		t.Fatalf("a completed operation must release its source pin, got %v", err)
	}

	if _, err := repo.Read(ctx, generation); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("expected the released generation to be gone, got %v", err)
	}
}

func setupTwoSessionSourceWithMissingDir(t *testing.T, ctx context.Context, server *gotmux.Server, repoDir string) string {
	t.Helper()

	first, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "fail-first", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, first)

	second, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "fail-second", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, second)

	observation, err := capture.Observe(ctx, server, []gotmux.SessionID{first.ID(), second.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	missingDir := filepath.Join(t.TempDir(), "missing")
	redirectPanesToMissingDirectory(t, &observation.Manifest, observation.Manifest.Sessions[1].ID, missingDir)

	if err := observation.Manifest.Validate(); err != nil {
		t.Fatalf("manifest validation failed: %v", err)
	}

	generation := commitCapture(t, ctx, repoDir, observation)
	newer := commitCapture(t, ctx, repoDir, observation)

	if newer == generation {
		t.Fatalf("expected two distinct generations, got %q twice", generation)
	}

	if err := first.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	if err := second.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	return generation
}

func setupUnrelatedEngineSession(t *testing.T, ctx context.Context, server *gotmux.Server) (gotmux.Session, []gotmux.WindowLinkInfo, gotmux.PaneInfo) {
	t.Helper()

	unrelated, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "unrelated-engine", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	_, unrelatedLinks, err := unrelated.WindowInfos(ctx)
	if err != nil || len(unrelatedLinks) != 1 {
		t.Fatalf("WindowInfos failed: %v %v", unrelatedLinks, err)
	}

	unrelatedWindow := unrelatedLinks[0].Handle().Window()

	unrelatedPane, err := unrelatedWindow.ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	unrelatedPaneInfo, err := unrelatedPane.Info(ctx)
	if err != nil {
		t.Fatalf("Pane Info failed: %v", err)
	}

	return unrelated, unrelatedLinks, unrelatedPaneInfo
}

func runFailingRestoreOnMissingDir(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	server *gotmux.Server,
	leaseRoot, generation string,
) engine.RestoreResult {
	t.Helper()

	result, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if err == nil {
		t.Fatal("expected the restore to fail on the missing working directory")
	}

	if !errors.Is(err, restore.ErrWorkingDirNotFound) {
		t.Fatalf("expected ErrWorkingDirNotFound, got %v", err)
	}

	if result.OperationID == "" {
		t.Fatal("operation identity must survive a failed attempt")
	}

	return result
}

func assertRetainedAndUnrelatedIntact(
	t *testing.T,
	ctx context.Context,
	server *gotmux.Server,
	result engine.RestoreResult,
	unrelated gotmux.Session,
	unrelatedLinks []gotmux.WindowLinkInfo,
	unrelatedPaneInfo gotmux.PaneInfo,
) {
	t.Helper()

	assertCreatedSessionRetained(t, ctx, server, result)
	assertUnrelatedSurvives(t, ctx, server, unrelated, unrelatedLinks, unrelatedPaneInfo)
}

func assertCreatedSessionRetained(t *testing.T, ctx context.Context, server *gotmux.Server, result engine.RestoreResult) {
	t.Helper()

	found, err := server.FindSession(ctx, "fail-first")
	if err != nil {
		t.Fatalf("retained session must survive, got %v", err)
	}

	if len(result.SessionsRestored) != 1 || result.SessionsRestored[0] != string(found.ID()) {
		t.Fatalf("expected restored session receipt for fail-first, got %+v", result.SessionsRestored)
	}
}

func assertUnrelatedSurvives(
	t *testing.T,
	ctx context.Context,
	server *gotmux.Server,
	unrelated gotmux.Session,
	unrelatedLinks []gotmux.WindowLinkInfo,
	unrelatedPaneInfo gotmux.PaneInfo,
) {
	t.Helper()

	found, err := server.FindSession(ctx, "unrelated-engine")
	if err != nil {
		t.Fatalf("unrelated session must survive: %v", err)
	}

	if found.ID() != unrelated.ID() {
		t.Fatalf("unrelated session identity changed: %v vs %v", found.ID(), unrelated.ID())
	}

	_, linksAfter, err := found.WindowInfos(ctx)
	if err != nil || len(linksAfter) != len(unrelatedLinks) {
		t.Fatalf("unrelated links changed: %v %v", linksAfter, err)
	}

	paneAfter, err := linksAfter[0].Handle().Window().ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	infoAfter, err := paneAfter.Info(ctx)
	if err != nil {
		t.Fatalf("Pane Info failed: %v", err)
	}

	if infoAfter.PID != unrelatedPaneInfo.PID || infoAfter.ID != unrelatedPaneInfo.ID {
		t.Fatalf("unrelated pane changed: %+v vs %+v", infoAfter, unrelatedPaneInfo)
	}
}

func assertFailureRetainedPinAndExplicitAbandonment(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	server *gotmux.Server,
	leaseRoot, generation, operationID string,
) {
	t.Helper()

	status, err := engine.ReconcileRestore(ctx, repo, operationID)
	if err != nil {
		t.Fatalf("ReconcileRestore failed: %v", err)
	}

	if status.State != "active" || status.Settled {
		t.Fatalf("expected an active, unsettled operation, got %+v", status)
	}

	if _, err := repo.Read(ctx, generation); err != nil {
		t.Fatalf("pinned source must stay readable: %v", err)
	}

	if err := repo.Delete(ctx, generation); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected the active operation to protect its source, got %v", err)
	}

	_, err = engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       operationID,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if !errors.Is(err, restore.ErrWorkingDirNotFound) {
		t.Fatalf("expected retained operation to resume without replay and fail at the same missing directory, got %v", err)
	}

	abandoned, err := engine.AbandonRestore(ctx, repo, operationID)
	if err != nil {
		t.Fatalf("AbandonRestore failed: %v", err)
	}

	if abandoned.State != "abandoned" {
		t.Fatalf("expected an abandoned operation, got %+v", abandoned)
	}

	if err := repo.Delete(ctx, generation); err != nil {
		t.Fatalf("abandonment must release the source pin, got %v", err)
	}
}

func buildAllSessionsPlan(t *testing.T, ctx context.Context, repo *store.Repository, generation string) restore.Plan {
	t.Helper()

	manifest, err := repo.Read(ctx, generation)
	if err != nil {
		t.Fatalf("repo.Read failed: %v", err)
	}

	selected := make([]string, 0, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		selected = append(selected, session.ID)
	}

	plan, err := restore.BuildPlan(manifest, selected)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	return plan
}

func assertSuccessLogRecords(t *testing.T, data []byte, repoDir string) {
	t.Helper()

	records := decodeLogRecords(t, data)
	started, finished := countRestoreLogStatuses(t, records)

	if started != 1 || finished != 1 {
		t.Fatalf("expected one start and one terminal record, got %d and %d", started, finished)
	}

	if bytes.Contains(data, []byte("log-source")) || bytes.Contains(data, []byte(repoDir)) {
		t.Fatalf("restore logs must not contain observed names or paths: %s", string(data))
	}
}

func countRestoreLogStatuses(t *testing.T, records []map[string]any) (int, int) {
	t.Helper()

	started, finished := 0, 0

	for _, record := range records {
		if record["component"] != "snapshot" || record["operation"] != "restore" {
			continue
		}

		switch record["status"] {
		case "started":
			started++
		case "complete", "partial":
			finished++

			assertTerminalRecordFields(t, record)
		}
	}

	return started, finished
}

func assertTerminalRecordFields(t *testing.T, record map[string]any) {
	t.Helper()

	if record["reason"] != "ok" && record["reason"] != "missing_requirements" {
		t.Fatalf("unexpected success reason: %+v", record)
	}

	if record["verified"] != true {
		t.Fatalf("expected a verified success record, got %+v", record)
	}
}

func runFailingRestoreForLogging(t *testing.T, logCtx context.Context, repo *store.Repository, server *gotmux.Server, leaseRoot string) {
	t.Helper()

	_, err := engine.Restore(logCtx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        "00000000000000000000000000000000",
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if err == nil {
		t.Fatal("expected restoring a missing generation to fail")
	}
}

func assertFailureLogRecords(t *testing.T, data []byte, repoDir, raw string) {
	t.Helper()

	failures := 0

	for _, record := range decodeLogRecords(t, data) {
		if record["status"] == "failed" {
			failures++

			if record["phase"] == "" || record["reason"] == "" {
				t.Fatalf("expected fixed phase and reason, got %+v", record)
			}
		}
	}

	if failures != 1 {
		t.Fatalf("expected exactly one failure record, got %d", failures)
	}

	if bytes.Contains(data, []byte(repoDir)) {
		t.Fatalf("failure logs must not contain paths: %s", string(data))
	}

	if !strings.Contains(raw, "snapshot restore failed") {
		t.Fatalf("expected the terminal failure message, got %s", raw)
	}
}

func TestEngineRestoreRefusesConfigMismatchOnSameOperationID(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "config-intent", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, live)

	generation := observeSessionAtMissingDirectory(t, ctx, server, repoDir, live)
	repo := openFixture(t, ctx, repoDir)

	if err := live.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	cfgOriginal := engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	}

	result, err := engine.Restore(ctx, repo, server, cfgOriginal)
	if !errors.Is(err, restore.ErrWorkingDirNotFound) {
		t.Fatalf("expected ErrWorkingDirNotFound, got %v", err)
	}

	if result.OperationID == "" {
		t.Fatal("expected preserved operation identity")
	}

	cfgModified := cfgOriginal
	cfgModified.OperationID = result.OperationID
	cfgModified.MissingWorkingDir = restore.MissingWorkingDirLaunch

	_, err = engine.Restore(ctx, repo, server, cfgModified)
	if err == nil {
		t.Fatal("expected config policy mismatch on same operation ID to fail")
	}

	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict for policy mismatch, got %v", err)
	}
}

func TestEngineRestoreVerificationFailureSingleOwningErrorRecord(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "schema-verify", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableSourceWindowRename(t, ctx, live)

	generation := observeSession(t, ctx, server, repoDir, live)
	repo := openFixture(t, ctx, repoDir)

	if err := live.Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	seq, err := gotmux.ParseSequence("rename-session tampered")
	if err != nil {
		t.Fatalf("ParseSequence failed: %v", err)
	}

	if err := server.GlobalHooks().SetWhole(ctx, "session-created", seq); err != nil {
		t.Fatalf("SetWhole hook failed: %v", err)
	}

	var buffer bytes.Buffer

	logger := zerolog.New(&buffer)
	logCtx := logger.WithContext(ctx)

	_, err = engine.Restore(logCtx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		Sessions:          nil,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
	})
	if err == nil {
		t.Fatal("expected verification to fail on slot mismatch")
	}

	records := decodeLogRecords(t, buffer.Bytes())
	assertSingleOwningFailureRecord(t, records, buffer.String(), repoDir)
}

func assertSingleOwningFailureRecord(t *testing.T, records []map[string]any, raw, repoDir string) {
	t.Helper()

	warns, errorsCount := 0, 0

	for _, record := range records {
		if record["component"] != "snapshot" || record["operation"] != "restore" {
			continue
		}

		if record["status"] == "failed" {
			errorsCount++

			assertFailedVerifyRecord(t, record)
		}

		if record["status"] == "partial" || record["level"] == "warn" {
			warns++
		}
	}

	if errorsCount != 1 {
		t.Fatalf("expected exactly one owning error record for terminal failure, got %d", errorsCount)
	}

	if warns != 0 {
		t.Fatalf("terminal failure must not be duplicated as warn record, got %d warns: %s", warns, raw)
	}

	if strings.Contains(raw, "schema-verify") || strings.Contains(raw, repoDir) {
		t.Fatalf("verification failure logs must not contain raw identities or paths: %s", raw)
	}
}

func assertFailedVerifyRecord(t *testing.T, record map[string]any) {
	t.Helper()

	if record["phase"] != "verify" {
		t.Fatalf("expected phase verify, got %+v", record)
	}

	if record["reason"] != "verification_failed" && record["reason"] != "restore_error" {
		t.Fatalf("expected fixed reason, got %+v", record)
	}
}
