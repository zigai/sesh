//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const actionLaunch = "launch"

func TestUncertainActionsCannotBeDispatchedAgain(t *testing.T) {
	for _, state := range []string{actionIssued, actionUnknown, actionConfirmed, actionVerified, actionConflict} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			op := pinnedLaunch(t, r)
			advanceAction(t, r, op.ID, state)

			reopened := openRepository(t, path)
			for _, next := range []string{actionPlanned, actionIssued} {
				if err := reopened.Transition(t.Context(), op.ID, "launch", state, next, nil); !errors.Is(err, ErrConflict) {
					t.Fatal("replayed potentially executed launch", err)
				}
			}
		})
	}
}

func pinnedLaunch(t *testing.T, r *Repository) Operation {
	t.Helper()
	source := commitFixture(t, r)

	op, err := r.Pin(t.Context(), source.ID, model.Bytes("explicit-target"), []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	return op
}

func advanceAction(t *testing.T, r *Repository, id, state string) {
	t.Helper()

	steps := []string{actionIssued}

	switch state {
	case actionUnknown, actionConfirmed, actionConflict:
		steps = append(steps, state)
	case actionVerified:
		steps = append(steps, actionConfirmed, actionVerified)
	}

	before := actionPlanned
	for _, next := range steps {
		if err := r.Transition(t.Context(), id, "launch", before, next, model.Bytes("original-runtime")); err != nil {
			t.Fatal(err)
		}

		before = next
	}
}

func TestOnlyNotSentPermitsRedispatch(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))

	op := pinnedLaunch(t, r)
	for _, step := range [][2]string{{actionPlanned, actionIssued}, {actionIssued, actionNotSent}, {actionNotSent, actionPlanned}, {actionPlanned, actionIssued}} {
		if err := r.Transition(t.Context(), op.ID, "launch", step[0], step[1], nil); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.Finish(t.Context(), op.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal("issued launch reported verified", err)
	}
}

func TestConcurrentLaunchDispatchHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	op := pinnedLaunch(t, r)
	other := openRepository(t, path)
	start := make(chan struct{})
	results := make(chan error, 2)

	var workers sync.WaitGroup
	for _, repository := range []*Repository{r, other} {
		workers.Go(func() {
			<-start

			results <- repository.Transition(t.Context(), op.ID, "launch", actionPlanned, actionIssued, nil)
		})
	}

	close(start)
	workers.Wait()
	close(results)

	winners, conflicts := 0, 0

	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}

	if winners != 1 || conflicts != 1 {
		t.Fatal("launch dispatch was not exclusive")
	}
}

func TestPinAndDeleteAreSerialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)
	commitFixture(t, r)
	other := openRepository(t, path)
	start := make(chan struct{})
	pinned, deleted := make(chan error, 1), make(chan error, 1)

	var workers sync.WaitGroup
	workers.Go(func() {
		<-start

		_, err := r.Pin(t.Context(), source.ID, model.Bytes("target"), []Action{{ID: "launch", Kind: actionLaunch, State: actionPlanned}})
		pinned <- err
	})
	workers.Go(func() { <-start; deleted <- other.Delete(t.Context(), source.ID) })
	close(start)
	workers.Wait()

	pinErr, deleteErr := <-pinned, <-deleted
	if pinErr == nil {
		if !errors.Is(deleteErr, ErrConflict) {
			t.Fatal("delete ignored active recovery pin", deleteErr)
		}

		if _, err := r.Read(t.Context(), source.ID); err != nil {
			t.Fatal("pinned source lost", err)
		}

		return
	}

	if !errors.Is(pinErr, ErrUnavailable) || deleteErr != nil {
		t.Fatal("unexpected concurrent outcomes", pinErr, deleteErr)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Operations) != 0 {
		t.Fatal("pin registered after source deletion")
	}
}

func TestReconcileOperationAfterInterruptedRegistration(t *testing.T) {
	for _, phase := range []string{faultCatalogSynced, faultCatalogReplaced} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			source := commitFixture(t, r)
			interruptCatalogPublication(r, phase)

			op, err := r.Pin(t.Context(), source.ID, model.Bytes("target"), []Action{{ID: "launch", Kind: actionLaunch, State: actionPlanned}})
			if !errors.Is(err, errInjected) || !validID(op.ID) {
				t.Fatal("interrupted registration lost its identity", err)
			}

			assertReconciledOperation(t, path, op, phase == faultCatalogSynced)
		})
	}
}

func TestReconcileOperationAfterInterruptedUpdate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		before    string
		after     string
		operation string
		apply     func(context.Context, *Repository, string) error
	}{
		{name: "dispatch", before: actionPlanned, after: actionIssued, operation: "active", apply: func(ctx context.Context, r *Repository, id string) error {
			return r.Transition(ctx, id, "launch", actionPlanned, actionIssued, nil)
		}},
		{name: "uncertain launch", before: actionIssued, after: actionUnknown, operation: "active", apply: func(ctx context.Context, r *Repository, id string) error {
			return r.Transition(ctx, id, "launch", actionIssued, actionUnknown, nil)
		}},
		{name: "completion", before: actionVerified, after: actionVerified, operation: "complete", apply: func(ctx context.Context, r *Repository, id string) error {
			return r.Finish(ctx, id, false)
		}},
		{name: "abandonment", before: actionIssued, after: actionIssued, operation: "abandoned", apply: func(ctx context.Context, r *Repository, id string) error {
			return r.Finish(ctx, id, true)
		}},
	} {
		for _, phase := range []string{faultCatalogSynced, faultCatalogReplaced} {
			t.Run(tt.name+"/"+phase, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "repository")
				r := openRepository(t, path)

				op := pinnedLaunch(t, r)
				if tt.before != actionPlanned {
					advanceAction(t, r, op.ID, tt.before)
					op.Actions[0].State, op.Actions[0].RuntimeID = tt.before, model.Bytes("original-runtime")
				}

				interruptCatalogPublication(r, phase)

				if err := tt.apply(t.Context(), r, op.ID); !errors.Is(err, errInjected) {
					t.Fatal("missing publication fault", err)
				}

				if phase == faultCatalogReplaced {
					op.State, op.Actions[0].State = tt.operation, tt.after
					if tt.operation == "active" {
						op.Actions[0].RuntimeID = nil
					}
				}

				assertReconciledOperation(t, path, op, false)
			})
		}
	}
}

func interruptCatalogPublication(r *Repository, phase string) {
	r.fault = func(current string) error {
		if current == phase {
			return errInjected
		}

		return nil
	}
}

func assertReconciledOperation(t *testing.T, path string, want Operation, missing bool) {
	t.Helper()

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	r := openRepository(t, path)

	got, err := r.ReconcileOperation(t.Context(), want.ID)
	if missing {
		assertRepositoryDiagnostic(t, err, "reconcile_operation", "snapshot_unavailable", ErrUnavailable)

		want = Operation{}
	} else if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatal(diff)
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(before, after); diff != "" {
		t.Fatal("reconciliation changed catalog", diff)
	}
}

func TestReconcileOperationSyncFailurePreservesUncertainty(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	op := pinnedLaunch(t, r)
	advanceAction(t, r, op.ID, actionUnknown)

	cause := &os.PathError{Op: "sync", Path: "/private/synthetic-path", Err: os.ErrPermission}
	r.fault = func(phase string) error {
		if phase == faultOperationReconcileSync {
			return errors.Join(cause, context.Canceled)
		}

		return nil
	}

	for _, id := range []string{op.ID, newID()} {
		got, err := r.ReconcileOperation(t.Context(), id)
		assertRepositoryDiagnostic(t, err, "reconcile_operation", "publication_uncertain", ErrUncertain)

		if !errors.Is(err, os.ErrPermission) || !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
			t.Fatal("reconciliation lost causes or reported certain absence", err)
		}

		if typed, ok := errors.AsType[*os.PathError](err); !ok || typed != cause {
			t.Fatal("lost typed sync failure")
		}

		if diff := cmp.Diff(Operation{}, got); diff != "" {
			t.Fatal("returned usable operation after failure", diff)
		}
	}

	r.fault = nil

	got, err := r.ReconcileOperation(t.Context(), op.ID)
	if err != nil || got.Actions[0].State != actionUnknown {
		t.Fatal("retry did not preserve uncertain launch", err)
	}
}

func TestReconcileOperationCancellationAndCorruptCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	op := pinnedLaunch(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := r.ReconcileOperation(ctx, op.ID)
	assertRepositoryDiagnostic(t, err, "reconcile_operation", "operation_canceled", context.Canceled)

	data, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if err := writeFile(r.root, ".catalog-"+newID(), data); err != nil {
		t.Fatal(err)
	}

	writeCatalogCorruption(t, path, []byte(`{"version":1}`))
	_, err = r.ReconcileOperation(t.Context(), op.ID)
	assertRepositoryDiagnostic(t, err, "reconcile_operation", "repository_corrupt", ErrCorrupt)
}

func TestReconcileOperationWaitsForWriterLock(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	op := pinnedLaunch(t, r)
	lock, err := r.root.Lock(t.Context(), "repository.lock")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	_, readErr := r.ReconcileOperation(ctx, op.ID)
	closeErr := lock.Close()

	assertRepositoryDiagnostic(t, readErr, "reconcile_operation", "operation_timed_out", context.DeadlineExceeded)

	if closeErr != nil {
		t.Fatal(closeErr)
	}

	if _, err := r.ReconcileOperation(t.Context(), op.ID); err != nil {
		t.Fatal("canceled lock wait leaked lock or corrupted journal", err)
	}
}

func TestReconcileOperationReturnsOwnedState(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	op := pinnedLaunch(t, r)
	advanceAction(t, r, op.ID, actionUnknown)

	want, err := r.ReconcileOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := r.ReconcileOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}

	changed.Target[0] = 'X'
	changed.Actions[0].State = actionPlanned
	changed.Actions[0].RuntimeID[0] = 'X'

	got, err := r.ReconcileOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal("caller changed authoritative journal state", diff)
	}
}
