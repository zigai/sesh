//go:build linux

package autosave

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func coordinatorGate(t *testing.T, repoPath string) error {
	t.Helper()

	cmd := NewDaemonCoordinatorCommand()
	if err := cmd.Flags().Set("repository", repoPath); err != nil {
		t.Fatalf("set repository flag: %v", err)
	}

	return ensureCoordinatorRepositoryReady(t.Context(), cmd, autosaveEnv{})
}

func operationState(t *testing.T, repoPath, id string) string {
	t.Helper()

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }()

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, op := range catalog.Operations {
		if op.ID == id {
			return op.State
		}
	}

	return ""
}

func TestCoordinatorStartsPastARestoreFromAnEarlierBoot(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)

	stale := snapfixture.RegisterRestore(t, repoPath, os.Getpid(), "00000000-1111-2222-3333-444444444444:1")

	if err := coordinatorGate(t, repoPath); err != nil {
		t.Fatalf("a restore aimed at a tmux server from an earlier boot must not block the coordinator: %v", err)
	}

	if state := operationState(t, repoPath, stale); state != "abandoned" {
		t.Fatalf("stale restore state = %q, want abandoned", state)
	}
}

func TestCoordinatorNamesARestoreThatMayStillRun(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)

	self, err := process.ReadIdentity(t.Context(), os.Getpid())
	if err != nil {
		t.Fatalf("read own identity: %v", err)
	}

	live := snapfixture.RegisterRestore(t, repoPath, os.Getpid(), self.String())

	err = coordinatorGate(t, repoPath)
	if !errors.Is(err, errCoordinatorRecoveryBarrier) || !errors.Is(err, automation.ErrActiveRestores) {
		t.Fatalf("gate error = %v, want the active-restore barrier", err)
	}

	if !strings.Contains(err.Error(), live) || !strings.Contains(err.Error(), "sesh snapshot recover restore abandon "+live) {
		t.Fatalf("the error must name the restore and how to abandon it: %v", err)
	}

	if state := operationState(t, repoPath, live); state != "active" {
		t.Fatalf("a restore whose server may still run must stay active, got %q", state)
	}
}

func TestCoordinatorRefusesUnresolvedSave(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "repository")

	repository, err := store.Open(t.Context(), repositoryPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	attempt, err := repository.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if err := repository.Fail(t.Context(), attempt.ID); err != nil {
		t.Fatalf("Fail failed: %v", err)
	}

	if err := repository.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	cmd := NewDaemonCoordinatorCommand()
	if err := cmd.Flags().Set("repository", repositoryPath); err != nil {
		t.Fatalf("set repository flag: %v", err)
	}

	err = ensureCoordinatorRepositoryReady(t.Context(), cmd, autosaveEnv{})
	if !errors.Is(err, errCoordinatorRecoveryBarrier) {
		t.Fatalf("ensureCoordinatorRepositoryReady error = %v, want recovery barrier", err)
	}
}
