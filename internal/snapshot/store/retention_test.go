//go:build linux || darwin

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestRetainedSourceReleasedOnlyAfterVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	op, err := r.Pin(t.Context(), source.ID, model.Bytes("synthetic-target"), []Action{{ID: "launch", Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	for range 7 {
		commitFixture(t, r)
	}

	reopened := openRepository(t, path)
	if _, err := reopened.Retain(t.Context(), minimumRetained); err != nil {
		t.Fatal(err)
	}

	if _, err := reopened.Read(t.Context(), source.ID); err != nil {
		t.Fatal("active recovery source pruned", err)
	}

	for _, step := range [][2]string{{actionPlanned, actionIssued}, {actionIssued, actionUnknown}, {actionUnknown, actionConfirmed}, {actionConfirmed, actionVerified}} {
		if err := reopened.Transition(t.Context(), op.ID, "launch", step[0], step[1], model.Bytes("runtime-receipt")); err != nil {
			t.Fatal(err)
		}
	}

	if err := reopened.Finish(t.Context(), op.ID, false); err != nil {
		t.Fatal(err)
	}

	removed, err := reopened.Retain(t.Context(), minimumRetained)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{source.ID}, removed); diff != "" {
		t.Fatal(diff)
	}

	if _, err := reopened.Read(t.Context(), source.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("retired generation still selectable", err)
	}

	if _, err := os.Stat(filepath.Join(path, "generations", source.ID, "manifest.json")); err != nil {
		t.Fatal("recovery bytes destroyed", err)
	}
}

func TestRetainRejectsCountBelowMinimum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	for range 6 {
		commitFixture(t, r)
	}

	cases := []int{0, 1, 4, -1}
	for _, count := range cases {
		removed, err := r.Retain(t.Context(), count)
		assertRepositoryDiagnostic(t, err, "retain", "repository_conflict", ErrConflict)

		if len(removed) != 0 {
			t.Fatalf("expected nil removed on conflict error, got %v", removed)
		}
	}
}

func TestRetainPublicationFaultDoesNotReportRemovals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	for range 8 {
		commitFixture(t, r)
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	removed, err := r.Retain(t.Context(), minimumRetained)
	assertRepositoryDiagnostic(t, err, "retain", "publication_uncertain", ErrUncertain)

	if len(removed) != 0 {
		t.Fatalf("expected nil removed on uncertain publication, got %v", removed)
	}
}

func TestDeleteRejectsCurrentOrPinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	gen1 := commitFixture(t, r)
	gen2 := commitFixture(t, r)

	err := r.Delete(t.Context(), gen2.ID)
	assertRepositoryDiagnostic(t, err, "delete", "repository_conflict", ErrConflict)

	op, err := r.Pin(t.Context(), gen1.ID, model.Bytes("target"), []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	err = r.Delete(t.Context(), gen1.ID)
	assertRepositoryDiagnostic(t, err, "delete", "repository_conflict", ErrConflict)

	err = r.Delete(t.Context(), "0123456789abcdef0123456789abcdef")
	assertRepositoryDiagnostic(t, err, "delete", "snapshot_unavailable", ErrUnavailable)

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatal(err)
	}

	if err := r.Delete(t.Context(), gen1.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(t.Context(), gen1.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable after delete, got %v", err)
	}
}
