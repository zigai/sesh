//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samber/oops"
)

func TestPublicRepositoryDiagnostics(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	_, readErr := r.Read(t.Context(), "private-snapshot-name")
	assertRepositoryDiagnostic(t, readErr, "read", "snapshot_unavailable", ErrUnavailable)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, beginErr := r.Begin(ctx)
	assertRepositoryDiagnostic(t, beginErr, "begin", "operation_canceled", context.Canceled)
	source := commitFixture(t, r)
	deleteErr := r.Delete(t.Context(), source.ID)
	assertRepositoryDiagnostic(t, deleteErr, "delete", "repository_conflict", ErrConflict)
	_, currentErr := r.Current(t.Context())
	assertRepositoryDiagnostic(t, currentErr, "current", "recovery_required", ErrRecoveryRequired)
}

func TestUncertainPublicationRetainsTypedCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	cause := &os.PathError{Op: "sync", Path: "/private/synthetic-path", Err: os.ErrPermission}
	r.fault = func(phase string) error {
		if phase == faultCatalogReplaced {
			return cause
		}

		return nil
	}
	m, artifacts := snapshotFixture()
	_, err = r.Commit(t.Context(), attempt.ID, m, artifacts)
	assertRepositoryDiagnostic(t, err, "commit", "publication_uncertain", ErrUncertain)

	if !errors.Is(err, os.ErrPermission) {
		t.Fatal("lost underlying permission identity")
	}

	got, ok := errors.AsType[*os.PathError](err)
	if !ok || got != cause {
		t.Fatal("lost typed publication cause")
	}

	assertReconciledPublication(t, path, attempt.ID)
}

func assertReconciledPublication(t *testing.T, path, id string) {
	t.Helper()

	reopened := openRepository(t, path)
	m, artifacts := snapshotFixture()

	generation, err := reopened.Commit(t.Context(), id, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	if generation.ID != id {
		t.Fatal("reconciliation changed generation identity")
	}

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Generations) != 1 || len(catalog.Attempts) != 1 || catalog.Attempts[0].State != "incomplete" {
		t.Fatal("reconciliation duplicated or changed committed snapshot")
	}

	if err := reopened.Fail(t.Context(), id); !errors.Is(err, ErrConflict) {
		t.Fatal("committed attempt could be marked failed")
	}
}

func assertRepositoryDiagnostic(t *testing.T, err error, operation, code string, sentinel error) {
	t.Helper()

	if !errors.Is(err, sentinel) {
		t.Fatalf("lost error identity: %v", err)
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok {
		t.Fatal("missing structured repository error")
	}

	layers := diagnostic.Layers()
	if len(layers) == 0 || layers[0].Domain != "snapshot.store" || layers[0].Code != code {
		t.Fatal("wrong owning error metadata")
	}

	if len(layers[0].Context) != 1 || layers[0].Context["operation"] != operation {
		t.Fatal("unexpected error context")
	}

	if diagnostic.Stacktrace() == "" {
		t.Fatal("missing diagnostic stack")
	}
}
