package automation_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func TestInspectStartupState_UnresolvedAttemptKeepsRecoveryBarrier(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	repository, err := store.Open(ctx, filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() {
		if err := repository.Close(); err != nil {
			t.Fatalf("repository.Close failed: %v", err)
		}
	}()

	attempt, err := repository.Begin(ctx)
	if err != nil {
		t.Fatalf("repository.Begin failed: %v", err)
	}

	if err := repository.Fail(ctx, attempt.ID); err != nil {
		t.Fatalf("repository.Fail failed: %v", err)
	}

	state, err := automation.InspectStartupState(ctx, repository)
	if err != nil {
		t.Fatalf("InspectStartupState failed: %v", err)
	}

	if !state.RepositoryPresent {
		t.Fatal("RepositoryPresent = false, want true for an opened repository")
	}

	if !state.RecoveryRequired {
		t.Fatalf("state = %+v, want an unresolved recovery barrier", state)
	}

	if state.ObservationOnly {
		t.Fatal("a failed attempt must not be reported as an observational save")
	}

	if state.Generation != "" {
		t.Fatalf("Generation = %q, want empty when no generation is readable", state.Generation)
	}

	admission := automation.EvaluateStartupRestore(automation.StartupState{Enabled: true, RepositoryPresent: state.RepositoryPresent, RecoveryRequired: state.RecoveryRequired, ObservationOnly: state.ObservationOnly, Generation: state.Generation})
	if admission.Allowed || admission.Reason != automation.ReasonRecoveryRequired {
		t.Fatalf("admission = %+v, want refusal with recovery_required", admission)
	}
}

func TestInspectStartupState_InterruptedSaveAdmitsNewestGeneration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)

	repository, err := store.Open(ctx, repoPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() {
		if err := repository.Close(); err != nil {
			t.Fatalf("repository.Close failed: %v", err)
		}
	}()

	catalog, err := repository.Inspect(ctx)
	if err != nil {
		t.Fatalf("repository.Inspect failed: %v", err)
	}

	newest := catalog.Current

	if _, err := repository.Begin(ctx); err != nil {
		t.Fatalf("repository.Begin failed: %v", err)
	}

	state, err := automation.InspectStartupState(ctx, repository)
	if err != nil {
		t.Fatalf("InspectStartupState failed: %v", err)
	}

	state.Enabled, state.Practical = true, true

	admission := automation.EvaluateStartupRestore(state)
	if !admission.Allowed || admission.Generation != newest {
		t.Fatalf("admission = %+v, want the newest saved generation %s admitted", admission, newest)
	}
}

func TestInspectStartupState_EmptyRepositoryHasNoGeneration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	repository, err := store.Open(ctx, filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() {
		if err := repository.Close(); err != nil {
			t.Fatalf("repository.Close failed: %v", err)
		}
	}()

	state, err := automation.InspectStartupState(ctx, repository)
	if err != nil {
		t.Fatalf("InspectStartupState failed: %v", err)
	}

	if state.Generation != "" || state.RecoveryRequired || state.ObservationOnly {
		t.Fatalf("state = %+v, want an empty repository state", state)
	}
}

func TestInspectStartupState_RequiresRepository(t *testing.T) {
	t.Parallel()

	if _, err := automation.InspectStartupState(context.Background(), nil); err == nil {
		t.Fatal("InspectStartupState accepted a nil repository")
	}
}
